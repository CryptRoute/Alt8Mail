package control

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var relayHostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)

type relayConfig struct {
	Enabled        bool
	Host           string
	Port           int
	Username       string
	Password       string
	Security       string
	LastTestAt     *time.Time
	LastTestStatus string
	LastTestDetail string
}

func (s *Server) loadRelay(ctx context.Context) (relayConfig, error) {
	var config relayConfig
	var sealed string
	err := s.db.QueryRow(ctx, `
		SELECT enabled, host, port, username, password_sealed, security,
		       last_test_at, last_test_status, last_test_detail
		FROM control.relay_settings WHERE id=true
	`).Scan(&config.Enabled, &config.Host, &config.Port, &config.Username, &sealed,
		&config.Security, &config.LastTestAt, &config.LastTestStatus, &config.LastTestDetail)
	if err != nil {
		return config, err
	}
	if sealed != "" {
		config.Password, _ = s.secrets.Open(sealed)
	}
	return config, nil
}

func (s *Server) handleRelayGet(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	config, err := s.loadRelay(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": config.Enabled, "host": config.Host, "port": config.Port,
		"username": config.Username, "security": config.Security,
		"hasPassword": config.Password != "", "lastTestAt": config.LastTestAt,
		"lastTestStatus": config.LastTestStatus, "lastTestDetail": config.LastTestDetail,
	})
}

func (s *Server) handleRelaySave(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Enabled       bool   `json:"enabled"`
		Host          string `json:"host"`
		Port          int    `json:"port"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		ClearPassword bool   `json:"clearPassword"`
		Security      string `json:"security"`
	}
	if err := decodeJSON(w, r, &body, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	body.Host = strings.TrimSpace(strings.ToLower(body.Host))
	if body.Enabled && (!relayHostPattern.MatchString(body.Host) || body.Port < 1 || body.Port > 65535) {
		writeError(w, http.StatusBadRequest, "RELAY", "Relay host and port are invalid")
		return
	}
	if body.Security != "starttls" && body.Security != "tls" && body.Security != "none" {
		writeError(w, http.StatusBadRequest, "RELAY", "Choose STARTTLS, implicit TLS or no TLS")
		return
	}
	if strings.ContainsAny(body.Username+body.Password, "\r\n") {
		writeError(w, http.StatusBadRequest, "RELAY", "Relay credentials cannot contain newlines")
		return
	}
	current, _ := s.loadRelay(r.Context())
	sealed := ""
	if !body.ClearPassword {
		if body.Password != "" {
			var err error
			sealed, err = s.secrets.Seal(body.Password)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "RELAY", "Could not protect the relay password")
				return
			}
		} else if current.Password != "" {
			sealed, _ = s.secrets.Seal(current.Password)
		}
	}
	_, err := s.db.Exec(r.Context(), `
		UPDATE control.relay_settings
		SET enabled=$1, host=$2, port=$3, username=$4, password_sealed=$5, security=$6,
		    updated_by=$7, updated_at=now()
		WHERE id=true
	`, body.Enabled, body.Host, body.Port, body.Username, sealed, body.Security, sess.User.ID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	changed, err := s.writeRelayConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RELAY", "Relay saved, but Postfix configuration failed: "+err.Error())
		return
	}
	if changed {
		_ = reloadPostfix(r.Context())
	}
	s.actorAudit(r, sess, "delivery.relay_updated", body.Host, "Updated outbound relay configuration",
		map[string]any{"enabled": body.Enabled, "host": body.Host, "port": body.Port, "security": body.Security})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "postfixChanged": changed})
}

func (s *Server) handleRelayTest(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	config, err := s.loadRelay(r.Context())
	if err != nil {
		writeFromError(w, err)
		return
	}
	if config.Host == "" || config.Port == 0 {
		writeError(w, http.StatusBadRequest, "RELAY", "Configure a relay host before testing")
		return
	}
	detail, testErr := testRelayConnection(config)
	status := "ok"
	errText := ""
	if testErr != nil {
		status, errText = "failed", testErr.Error()
	}
	_, _ = s.db.Exec(r.Context(), `
		UPDATE control.relay_settings SET last_test_at=now(), last_test_status=$1, last_test_detail=$2 WHERE id=true
	`, status, truncateText(detail+" "+errText, 2000))
	s.actorAudit(r, sess, "delivery.relay_test", config.Host, "Tested outbound relay",
		map[string]any{"status": status, "detail": detail})
	writeJSON(w, http.StatusOK, map[string]any{"ok": testErr == nil, "status": status, "detail": detail, "error": errText})
}

func testRelayConnection(config relayConfig) (string, error) {
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	detail := "Connected to " + address
	var client *smtp.Client
	var err error
	if config.Security == "tls" {
		conn, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: 12 * time.Second}, "tcp", address, &tls.Config{
			ServerName: config.Host, MinVersion: tls.VersionTLS12,
		})
		if dialErr != nil {
			return detail, fmt.Errorf("TLS connection failed: %w", dialErr)
		}
		client, err = smtp.NewClient(conn, config.Host)
	} else {
		client, err = smtp.Dial(address)
	}
	if err != nil {
		return detail, fmt.Errorf("SMTP connection failed: %w", err)
	}
	defer client.Close()
	if config.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return detail + "; STARTTLS not advertised", fmt.Errorf("relay does not advertise STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return detail, fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if config.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return detail, fmt.Errorf("relay does not advertise AUTH")
		}
		if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
			return detail, fmt.Errorf("SMTP authentication failed: %w", err)
		}
		detail += "; authentication accepted"
	}
	if err := client.Noop(); err != nil {
		return detail, fmt.Errorf("relay NOOP failed: %w", err)
	}
	_ = client.Quit()
	return detail + "; NOOP accepted", nil
}

func (s *Server) writeRelayConfig(ctx context.Context) (bool, error) {
	config, err := s.loadRelay(ctx)
	if err != nil {
		return false, err
	}
	changed := false
	saslPath := filepath.Join(s.cfg.PostfixDir, "sasl_passwd")
	if config.Enabled && config.Host != "" {
		line := fmt.Sprintf("[%s]:%d %s:%s\n", config.Host, config.Port, config.Username, config.Password)
		fileChanged, err := writeManagedFile(saslPath, line, 0o600, 0, 0)
		if err != nil {
			return changed, err
		}
		changed = changed || fileChanged
		if _, err := runCommand(ctx, 15*time.Second, "postmap", saslPath); err != nil {
			return changed, fmt.Errorf("postmap sasl_passwd failed")
		}
		relayHost := fmt.Sprintf("[%s]:%d", config.Host, config.Port)
		for key, value := range map[string]string{
			"relayhost":                  relayHost,
			"smtp_sasl_auth_enable":      "yes",
			"smtp_sasl_password_maps":    "hash:" + saslPath,
			"smtp_sasl_security_options": "noanonymous",
			"smtp_tls_wrappermode":       boolSetting(config.Security == "tls"),
			"smtp_tls_security_level":    map[string]string{"starttls": "encrypt", "tls": "encrypt", "none": "none"}[config.Security],
			"smtp_use_tls":               boolSetting(config.Security != "none"),
		} {
			settingChanged, err := postconfEnsure(ctx, key, value)
			if err != nil {
				return changed, err
			}
			changed = changed || settingChanged
		}
	} else {
		if _, err := os.Stat(saslPath); err == nil {
			changed = true
		}
		if err := removeManagedFile(saslPath); err != nil {
			return changed, err
		}
		if _, err := os.Stat(saslPath + ".db"); err == nil {
			changed = true
		}
		_ = removeManagedFile(saslPath + ".db")
		for key, value := range map[string]string{
			"relayhost":               "",
			"smtp_sasl_auth_enable":   "no",
			"smtp_sasl_password_maps": "",
			"smtp_tls_wrappermode":    "no",
			"smtp_tls_security_level": "may",
			"smtp_use_tls":            "yes",
		} {
			settingChanged, err := postconfEnsure(ctx, key, value)
			if err != nil {
				return changed, err
			}
			changed = changed || settingChanged
		}
	}
	return changed, nil
}

func boolSetting(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func postconfEnsure(ctx context.Context, key, value string) (bool, error) {
	result, err := runCommand(ctx, 10*time.Second, "postconf", "-h", key)
	if err == nil && strings.TrimSpace(result.Stdout) == value {
		return false, nil
	}
	result, err = runCommand(ctx, 15*time.Second, "postconf", "-e", key+"="+value)
	if err != nil {
		return false, fmt.Errorf("postconf %s: %s", key, strings.TrimSpace(result.Stderr))
	}
	return true, nil
}
