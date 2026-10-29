package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var systemAccountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

type terminalSession struct {
	ticket   string
	username string
	client   *ssh.Client
	session  *ssh.Session
	stdin    io.WriteCloser
	stdout   io.Reader
	stderr   io.Reader
	created  time.Time
	expires  time.Time
	once     sync.Once
}

func (t *terminalSession) close() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.session != nil {
			_ = t.session.Close()
		}
		if t.client != nil {
			_ = t.client.Close()
		}
	})
}

func (s *Server) registerTerminalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /control/api/infra/terminal/info", s.require("server.manage", s.handleTerminalInfo))
	mux.HandleFunc("POST /control/api/infra/terminal/sessions", s.require("server.manage", s.handleTerminalSessionCreate))
	mux.HandleFunc("GET /control/api/infra/terminal/ws", s.require("server.manage", s.handleTerminalWebSocket))
}

func (s *Server) handleTerminalInfo(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	port, source, err := detectSSHPort(r.Context())
	info := map[string]any{
		"host": "127.0.0.1", "loopbackOnly": true,
		"passwordAuthentication": false, "keyAuthentication": true,
	}
	if err != nil {
		info["error"] = err.Error()
	} else {
		info["port"] = port
		info["portSource"] = source
		info["endpoint"] = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		password, pubkey := sshdAuthModes(r.Context(), port)
		info["passwordAuthentication"] = password
		info["keyAuthentication"] = pubkey
	}
	info["hostname"] = actualHostname()
	writeJSON(w, http.StatusOK, info)
}

func detectSSHPort(ctx context.Context) (int, string, error) {
	if result, err := runCommand(ctx, 10*time.Second, "sshd", "-T"); err == nil {
		for _, line := range strings.Split(result.Stdout, "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) == 2 && strings.EqualFold(fields[0], "port") {
				if value, parseErr := strconv.Atoi(fields[1]); parseErr == nil && value > 0 && value <= 65535 {
					return value, "sshd -T", nil
				}
			}
		}
	}
	configFiles := []string{"/etc/ssh/sshd_config"}
	extra, _ := filepath.Glob("/etc/ssh/sshd_config.d/*.conf")
	sort.Strings(extra)
	configFiles = append(configFiles, extra...)
	for _, path := range configFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) == 2 && strings.EqualFold(fields[0], "Port") {
				if value, parseErr := strconv.Atoi(fields[1]); parseErr == nil && value > 0 && value <= 65535 {
					return value, path, nil
				}
			}
		}
	}
	if result, err := runCommand(ctx, 10*time.Second, "ss", "-ltnp"); err == nil {
		for _, line := range strings.Split(result.Stdout, "\n") {
			if !strings.Contains(strings.ToLower(line), "sshd") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			local := fields[3]
			if index := strings.LastIndex(local, ":"); index >= 0 {
				if value, parseErr := strconv.Atoi(local[index+1:]); parseErr == nil && value > 0 && value <= 65535 {
					return value, "ss listening socket", nil
				}
			}
		}
	}
	return 0, "", fmt.Errorf("could not detect the SSH listener port; verify sshd is running and its configuration is readable")
}

func sshdAuthModes(ctx context.Context, port int) (password bool, publicKey bool) {
	criteria := fmt.Sprintf("addr=127.0.0.1,user=root,laddr=127.0.0.1,lport=%d", port)
	result, err := runCommand(ctx, 10*time.Second, "sshd", "-T", "-C", criteria)
	if err != nil {
		return false, true
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "passwordauthentication":
			password = strings.EqualFold(fields[1], "yes")
		case "pubkeyauthentication":
			publicKey = strings.EqualFold(fields[1], "yes")
		}
	}
	return password, publicKey
}

func (s *Server) handleTerminalSessionCreate(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	var body struct {
		Username   string `json:"username"`
		AuthType   string `json:"authType"`
		Password   string `json:"password"`
		PrivateKey string `json:"privateKey"`
		Passphrase string `json:"passphrase"`
		Rows       int    `json:"rows"`
		Cols       int    `json:"cols"`
	}
	if err := decodeJSON(w, r, &body, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	username := strings.TrimSpace(body.Username)
	if !systemAccountPattern.MatchString(username) {
		writeError(w, http.StatusBadRequest, "USERNAME", "Enter a valid local system account name")
		return
	}
	if _, err := user.Lookup(username); err != nil {
		writeError(w, http.StatusBadRequest, "USERNAME", "That system account does not exist on this host")
		return
	}
	authType := strings.ToLower(strings.TrimSpace(body.AuthType))
	var authMethod ssh.AuthMethod
	switch authType {
	case "password":
		if body.Password == "" {
			writeError(w, http.StatusBadRequest, "PASSWORD", "Enter the system account password")
			return
		}
		authMethod = ssh.Password(body.Password)
	case "key":
		signer, err := parseTerminalKey(body.PrivateKey, body.Passphrase)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PRIVATE_KEY", err.Error())
			return
		}
		authMethod = ssh.PublicKeys(signer)
	default:
		writeError(w, http.StatusBadRequest, "AUTH", "Authentication must be password or key")
		return
	}
	s.termMu.Lock()
	activeTerminals := len(s.termSessions)
	s.termMu.Unlock()
	if activeTerminals >= 5 {
		writeError(w, http.StatusTooManyRequests, "TERMINAL_LIMIT", "Close an existing terminal session before opening another")
		return
	}
	port, source, err := detectSSHPort(r.Context())
	if err != nil {
		writeError(w, http.StatusConflict, "SSH_PORT", err.Error())
		return
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(r.Context(), "tcp", addr)
	if err != nil {
		writeError(w, http.StatusBadGateway, "SSH_CONNECT", "The local SSH service is not reachable on port "+strconv.Itoa(port)+": "+err.Error())
		return
	}
	config := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
		ClientVersion:   "SSH-2.0-Alt8Mail-Control",
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		writeError(w, http.StatusUnprocessableEntity, "SSH_AUTH", "SSH authentication failed: "+err.Error())
		return
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	sshSession, err := client.NewSession()
	if err != nil {
		client.Close()
		writeError(w, http.StatusBadGateway, "SSH_SESSION", "Could not open an SSH session: "+err.Error())
		return
	}
	rows, cols := body.Rows, body.Cols
	if rows < 10 || rows > 300 {
		rows = 40
	}
	if cols < 20 || cols > 400 {
		cols = 120
	}
	if err := sshSession.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}); err != nil {
		sshSession.Close()
		client.Close()
		writeError(w, http.StatusBadGateway, "SSH_PTY", "Could not allocate a pseudo-terminal: "+err.Error())
		return
	}
	stdin, err := sshSession.StdinPipe()
	if err != nil {
		sshSession.Close()
		client.Close()
		writeError(w, http.StatusBadGateway, "SSH_STDIN", "Could not open the SSH input stream: "+err.Error())
		return
	}
	stdout, err := sshSession.StdoutPipe()
	if err != nil {
		sshSession.Close()
		client.Close()
		writeError(w, http.StatusBadGateway, "SSH_STDOUT", "Could not open the SSH output stream: "+err.Error())
		return
	}
	stderr, err := sshSession.StderrPipe()
	if err != nil {
		// Some SSH servers merge stderr into the PTY stream. That is fine; use
		// an empty reader rather than refusing an otherwise usable shell.
		stderr = strings.NewReader("")
	}
	if err := sshSession.Shell(); err != nil {
		sshSession.Close()
		client.Close()
		writeError(w, http.StatusBadGateway, "SSH_SHELL", "Could not start the SSH shell: "+err.Error())
		return
	}
	ticket, err := randomToken(32)
	if err != nil {
		sshSession.Close()
		client.Close()
		writeError(w, http.StatusInternalServerError, "TICKET", "Could not allocate a terminal ticket")
		return
	}
	terminal := &terminalSession{
		ticket: ticket, username: username, client: client, session: sshSession,
		stdin: stdin, stdout: stdout, stderr: stderr,
		created: time.Now().UTC(), expires: time.Now().Add(2 * time.Minute),
	}
	s.termMu.Lock()
	// Prune abandoned tickets while we hold the map lock.
	for key, existing := range s.termSessions {
		if time.Now().After(existing.expires) {
			delete(s.termSessions, key)
			go existing.close()
		}
	}
	s.termSessions[ticket] = terminal
	s.termMu.Unlock()
	time.AfterFunc(2*time.Minute, func() { s.expireTerminalTicket(ticket) })
	s.actorAudit(r, sess, "terminal.connected", username, "Opened an SSH terminal session",
		map[string]any{"port": port, "portSource": source, "authType": authType})
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "ticket": ticket, "username": username, "port": port, "portSource": source,
		"expiresAt": terminal.expires.Format(time.RFC3339),
		"message":   "SSH session established. Open the WebSocket terminal within two minutes.",
	})
}

func parseTerminalKey(raw, passphrase string) (ssh.Signer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("paste the private key for the system account")
	}
	keyBytes := []byte(raw)
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(passphrase))
		if err == nil {
			return signer, nil
		}
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, fmt.Errorf("this private key is encrypted; enter its passphrase")
		}
		return nil, fmt.Errorf("could not parse the private key: %w", err)
	}
	return signer, nil
}

func (s *Server) expireTerminalTicket(ticket string) {
	s.termMu.Lock()
	terminal := s.termSessions[ticket]
	if terminal != nil && time.Now().After(terminal.expires) {
		delete(s.termSessions, ticket)
	} else {
		terminal = nil
	}
	s.termMu.Unlock()
	if terminal != nil {
		terminal.close()
	}
}

func (s *Server) takeTerminalTicket(ticket string) *terminalSession {
	if strings.TrimSpace(ticket) == "" {
		return nil
	}
	s.termMu.Lock()
	terminal := s.termSessions[ticket]
	delete(s.termSessions, ticket)
	s.termMu.Unlock()
	if terminal == nil {
		return nil
	}
	if time.Now().After(terminal.expires) {
		terminal.close()
		return nil
	}
	return terminal
}

func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request, sess *sessionRecord) {
	if !sameWebSocketOrigin(r) {
		writeError(w, http.StatusForbidden, "ORIGIN", "WebSocket origin is not allowed")
		return
	}
	terminal := s.takeTerminalTicket(r.URL.Query().Get("ticket"))
	if terminal == nil {
		writeError(w, http.StatusGone, "TICKET", "That terminal ticket is missing, used or expired. Connect again.")
		return
	}
	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		terminal.close()
		http.Error(w, "WebSocket upgrade failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() {
		terminal.close()
		_ = ws.Close()
	}()
	// Reap the remote shell even if the browser disappears without a close
	// frame. The output goroutines close the socket after the pipes drain.
	go func() { _ = terminal.session.Wait() }()
	var streams sync.WaitGroup
	streams.Add(2)
	stream := func(reader io.Reader) {
		defer streams.Done()
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := reader.Read(buffer)
			if n > 0 {
				if err := ws.WriteBinary(buffer[:n]); err != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}
	go stream(terminal.stdout)
	go stream(terminal.stderr)
	go func() {
		streams.Wait()
		_ = ws.WriteClose(1000, "shell exited")
		_ = ws.Close()
	}()
	for {
		opcode, payload, err := ws.readFrame()
		if err != nil {
			return
		}
		if opcode == 0x2 {
			if _, err := terminal.stdin.Write(payload); err != nil {
				return
			}
			continue
		}
		var message struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Rows int    `json:"rows"`
			Cols int    `json:"cols"`
		}
		if err := json.Unmarshal(payload, &message); err != nil {
			continue
		}
		switch message.Type {
		case "input":
			if len(message.Data) > 256<<10 {
				continue
			}
			if _, err := terminal.stdin.Write([]byte(message.Data)); err != nil {
				return
			}
		case "resize":
			rows, cols := message.Rows, message.Cols
			if rows < 10 || rows > 300 || cols < 20 || cols > 400 {
				continue
			}
			_ = terminal.session.WindowChange(rows, cols)
		}
	}
}

func sameWebSocketOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
