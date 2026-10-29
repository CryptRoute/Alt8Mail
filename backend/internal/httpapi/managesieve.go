package httpapi

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// pushSieveScript installs the generated ruleset into Dovecot's Pigeonhole
// ManageSieve service. The structured rules remain the source of truth in the
// database; this makes them executable by the MTA instead of serving as a
// preview-only script.
func (s *Server) pushSieveScript(ctx context.Context, user userRecord, script string) error {
	if !s.mailAccountConfigured(user.Email) || !s.tenantPolicyFor(ctx, user.TenantID).Mail.IMAPAccess {
		return nil
	}
	loginUser, loginPassword, err := s.mailLoginForUser(user.Email)
	if err != nil {
		return err
	}
	tlsConfig, err := s.mailTLSConfig(s.cfg.ManageSieveTLSName)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", s.cfg.ManageSieveAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to ManageSieve: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)
	if err := readSieveResponse(reader); err != nil {
		return fmt.Errorf("ManageSieve greeting: %w", err)
	}
	if err := writeSieveCommand(conn, "STARTTLS"); err != nil {
		return err
	}
	if err := readSieveResponse(reader); err != nil {
		return fmt.Errorf("ManageSieve STARTTLS rejected: %w", err)
	}
	tlsConn := tls.Client(conn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("ManageSieve TLS handshake: %w", err)
	}
	defer tlsConn.Close()
	_ = tlsConn.SetDeadline(time.Now().Add(30 * time.Second))
	reader = bufio.NewReader(tlsConn)

	auth := "\x00" + loginUser + "\x00" + loginPassword
	authB64 := base64.StdEncoding.EncodeToString([]byte(auth))
	if err := writeSieveCommand(tlsConn, `AUTHENTICATE "PLAIN" "`+authB64+`"`); err != nil {
		return err
	}
	if err := readSieveResponse(reader); err != nil {
		return fmt.Errorf("ManageSieve authentication rejected: %w", err)
	}

	literal := fmt.Sprintf("PUTSCRIPT \"a8m\" {%d+}\r\n", len(script))
	if _, err := tlsConn.Write([]byte(literal)); err != nil {
		return err
	}
	if _, err := tlsConn.Write([]byte(script)); err != nil {
		return err
	}
	if _, err := tlsConn.Write([]byte("\r\n")); err != nil {
		return err
	}
	if err := readSieveResponse(reader); err != nil {
		return fmt.Errorf("ManageSieve rejected the script: %w", err)
	}
	if err := writeSieveCommand(tlsConn, `SETACTIVE "a8m"`); err != nil {
		return err
	}
	if err := readSieveResponse(reader); err != nil {
		return fmt.Errorf("ManageSieve could not activate the script: %w", err)
	}
	_ = writeSieveCommand(tlsConn, "LOGOUT")
	return nil
}

func writeSieveCommand(conn net.Conn, command string) error {
	_, err := conn.Write([]byte(command + "\r\n"))
	return err
}

func sieveLiteralSize(line string) int {
	trimmed := strings.TrimSpace(line)
	if !strings.HasSuffix(trimmed, "}") {
		return 0
	}
	open := strings.LastIndex(trimmed, "{")
	if open < 0 {
		return 0
	}
	spec := strings.TrimSuffix(trimmed[open+1:], "}")
	spec = strings.TrimSuffix(spec, "+")
	size, err := strconv.Atoi(spec)
	if err != nil || size < 1 || size > 1024*1024 {
		return 0
	}
	return size
}

func readSieveResponse(reader *bufio.Reader) error {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(upper, "OK"):
			return nil
		case strings.HasPrefix(upper, "NO"), strings.HasPrefix(upper, "BYE"):
			// Dovecot often returns the human-readable reason as a ManageSieve
			// literal after the status line (e.g. "NO {229}"). Read it so
			// operators see the real compiler error instead of a byte count.
			if size := sieveLiteralSize(line); size > 0 {
				payload := make([]byte, size)
				if _, err := io.ReadFull(reader, payload); err != nil {
					return fmt.Errorf("%s (literal read failed: %v)", line, err)
				}
				return fmt.Errorf("%s %s", line, strings.TrimSpace(string(payload)))
			}
			return fmt.Errorf("%s", line)
		case strings.HasPrefix(upper, "AUTHENTICATE"), strings.HasPrefix(upper, `"`):
			// Greeting/continuation metadata; keep reading until OK/NO.
			continue
		}
	}
}
