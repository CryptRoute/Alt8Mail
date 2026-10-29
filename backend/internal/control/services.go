package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,180}\.service$`)

func (s *Server) registerServiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/services", s.require("services.read", s.handleServices))
	mux.HandleFunc("POST /control/api/services/{unit}/{action}", s.require("services.manage", s.handleServiceAction))
	mux.HandleFunc("GET /control/api/services/config", s.require("services.read", s.handlePostfixConfigGet))
	mux.HandleFunc("PUT /control/api/services/config", s.require("services.manage", s.handlePostfixConfigSave))
	mux.HandleFunc("POST /control/api/services/config/reload", s.require("services.manage", s.handlePostfixReload))
	mux.HandleFunc("POST /control/api/services/config/restart", s.require("services.manage", s.handlePostfixRestart))
	mux.HandleFunc("GET /control/api/services/ports", s.require("services.read", s.handlePorts))
	mux.HandleFunc("POST /control/api/services/ports/preview", s.require("services.read", s.handlePortsPreview))
	mux.HandleFunc("PUT /control/api/services/ports", s.require("services.manage", s.handlePortsSave))
	mux.HandleFunc("GET /control/api/services/stack-ports", s.require("services.read", s.handleStackPorts))
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	services := s.probe.serviceStatuses(r.Context(), queryBool(r, "refresh", false))
	writeJSON(w, http.StatusOK, map[string]any{"services": services, "checkedAt": nowRFC3339()})
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	unit := strings.TrimSpace(r.PathValue("unit"))
	action := strings.ToLower(strings.TrimSpace(r.PathValue("action")))
	if !unitPattern.MatchString(unit) {
		writeError(w, http.StatusBadRequest, "UNIT", "Invalid systemd unit")
		return
	}
	if !allowedServiceUnit(unit) {
		writeError(w, http.StatusForbidden, "UNIT", "That unit is not managed by System Control")
		return
	}
	switch action {
	case "start", "stop", "restart", "reload", "enable", "disable":
	default:
		writeError(w, http.StatusBadRequest, "ACTION", "Action must be start, stop, restart, reload, enable or disable")
		return
	}
	if unit == "alt8mail-control.service" && (action == "stop" || action == "disable") {
		writeError(w, http.StatusBadRequest, "UNIT", "Use systemd on the server to stop the control service itself")
		return
	}
	result, err := runCommand(r.Context(), 60*time.Second, "systemctl", action, unit)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		writeError(w, http.StatusBadGateway, "SYSTEMD", "systemctl "+action+" failed: "+detail)
		return
	}
	_ = s.probe.serviceStatuses(r.Context(), true)
	s.hub.Broadcast("services.updated", map[string]any{"unit": unit, "action": action})
	s.actorAudit(r, sess, "service."+action, unit, "Ran systemctl "+action+" "+unit, map[string]any{"output": strings.TrimSpace(result.Stdout)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unit": unit, "action": action, "output": strings.TrimSpace(result.Stdout + "\n" + result.Stderr)})
}

func allowedServiceUnit(unit string) bool {
	for _, allowed := range coreServiceUnits {
		if unit == allowed {
			return true
		}
	}
	switch unit {
	case "postfix.service", "postfix@-.service", "dovecot.service", "rspamd.service",
		"clamav-daemon.service", "clamav-freshclam.service", "nginx.service",
		"postgresql@15-main.service", "redis-server.service", "fail2ban.service",
		"cron.service", "chrony.service", "alt8mail-control.service",
		"a8m-server.service", "a8m-admin.service", "rclone-s3.service":
		return true
	}
	return false
}

func (s *Server) handlePostfixConfigGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	file := r.URL.Query().Get("file")
	path, err := s.postfixConfigPath(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FILE", err.Error())
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file": file, "path": path, "content": string(raw), "sha256": fileDigest(path),
		"mainCfPath":   filepath.Join(s.cfg.PostfixDir, "main.cf"),
		"masterCfPath": filepath.Join(s.cfg.PostfixDir, "master.cf"),
	})
}

func (s *Server) postfixConfigPath(file string) (string, error) {
	switch strings.TrimSpace(file) {
	case "main.cf":
		return filepath.Join(s.cfg.PostfixDir, "main.cf"), nil
	case "master.cf":
		return filepath.Join(s.cfg.PostfixDir, "master.cf"), nil
	default:
		return "", fmt.Errorf("file must be main.cf or master.cf")
	}
}

func (s *Server) handlePostfixConfigSave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		File         string `json:"file"`
		Content      string `json:"content"`
		ExpectedHash string `json:"expectedHash"`
	}
	if err := decodeJSON(w, r, &body, 2<<20); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	path, err := s.postfixConfigPath(body.File)
	if err != nil {
		writeError(w, http.StatusBadRequest, "FILE", err.Error())
		return
	}
	if len(body.Content) > 512<<10 {
		writeError(w, http.StatusBadRequest, "FILE", "Configuration file is too large")
		return
	}
	if strings.TrimSpace(body.ExpectedHash) != "" && fileDigest(path) != body.ExpectedHash {
		writeError(w, http.StatusConflict, "CONFLICT", "The file changed since it was loaded. Reload and review the current version.")
		return
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		writeFromError(w, err)
		return
	}
	backupDir := filepath.Join(s.cfg.BackupDir, "postfix")
	_ = os.MkdirAll(backupDir, 0o700)
	backup := filepath.Join(backupDir, filepath.Base(path)+"."+time.Now().UTC().Format("20060102-150405"))
	_ = os.WriteFile(backup, previous, 0o600)
	if _, err := writeManagedFile(path, body.Content, 0o644, 0, 0); err != nil {
		writeFromError(w, err)
		return
	}
	result, checkErr := runCommand(r.Context(), 30*time.Second, "postfix", "check")
	if checkErr != nil {
		_ = os.WriteFile(path, previous, 0o644)
		writeError(w, http.StatusBadRequest, "POSTFIX", "postfix check rejected the edit: "+strings.TrimSpace(result.Stderr+"\n"+result.Stdout))
		return
	}
	reloadResult, reloadErr := runCommand(r.Context(), 30*time.Second, "postfix", "reload")
	if reloadErr != nil {
		writeError(w, http.StatusBadGateway, "RELOAD", "Configuration saved but reload failed: "+strings.TrimSpace(reloadResult.Stderr))
		return
	}
	s.actorAudit(r, sess, "services.postfix_config_saved", body.File, "Edited Postfix configuration", map[string]any{"backup": backup})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backup": backup, "sha256": fileDigest(path)})
}

func (s *Server) handlePostfixReload(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 30*time.Second, "postfix", "reload")
	if err != nil {
		writeError(w, http.StatusBadGateway, "RELOAD", strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "services.postfix_reload", "Postfix", "Reloaded Postfix", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePostfixRestart(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 60*time.Second, "systemctl", "restart", "postfix@-.service")
	if err != nil {
		writeError(w, http.StatusBadGateway, "RESTART", strings.TrimSpace(result.Stderr))
		return
	}
	s.actorAudit(r, sess, "services.postfix_restart", "Postfix", "Restarted Postfix", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleStackPorts(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	result, err := runCommand(r.Context(), 10*time.Second, "ss", "-ltnp")
	if err != nil {
		writeError(w, http.StatusBadGateway, "PORTS", "Could not read listening sockets")
		return
	}
	ports := []map[string]any{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] == "State" {
			continue
		}
		local := fields[3]
		port := ""
		if index := strings.LastIndex(local, ":"); index >= 0 {
			port = local[index+1:]
		}
		process := ""
		if len(fields) > 5 {
			process = strings.Join(fields[5:], " ")
		}
		ports = append(ports, map[string]any{"local": local, "port": port, "process": process})
	}
	writeJSON(w, http.StatusOK, map[string]any{"listeners": ports})
}

var _ = context.Background
