package httpapi

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// minimalPOP3 is a deliberately small POP3 client for mailbox migration. It
// only implements the commands needed to validate a login and download
// messages (USER, PASS, STAT, LIST, RETR, QUIT) with TLS or STLS.
type minimalPOP3 struct {
	conn net.Conn
	text *textproto.Reader
	buf  *bufio.Writer
}

func dialPOP3(host string, port int, security string) (*minimalPOP3, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, 12*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to POP3 server: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	client := &minimalPOP3{conn: conn, text: textproto.NewReader(bufio.NewReader(conn)), buf: bufio.NewWriter(conn)}
	if err := client.expectOK("greeting"); err != nil {
		conn.Close()
		return nil, err
	}
	if security == "ssl" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("POP3 TLS handshake: %w", err)
		}
		client.conn = tlsConn
		client.text = textproto.NewReader(bufio.NewReader(tlsConn))
		client.buf = bufio.NewWriter(tlsConn)
	} else if security == "starttls" {
		if _, err := client.command("STLS"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("POP3 STLS rejected: %w", err)
		}
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("POP3 STARTTLS handshake: %w", err)
		}
		client.conn = tlsConn
		client.text = textproto.NewReader(bufio.NewReader(tlsConn))
		client.buf = bufio.NewWriter(tlsConn)
	}
	return client, nil
}

func (p *minimalPOP3) close() {
	_, _ = p.command("QUIT")
	_ = p.conn.Close()
}

func (p *minimalPOP3) expectOK(context string) error {
	line, err := p.text.ReadLine()
	if err != nil {
		return fmt.Errorf("POP3 %s: %w", context, err)
	}
	if !strings.HasPrefix(line, "+OK") {
		return fmt.Errorf("POP3 %s: %s", context, strings.TrimSpace(line))
	}
	return nil
}

func (p *minimalPOP3) command(format string, args ...any) (string, error) {
	if _, err := fmt.Fprintf(p.buf, format+"\r\n", args...); err != nil {
		return "", err
	}
	if err := p.buf.Flush(); err != nil {
		return "", err
	}
	line, err := p.text.ReadLine()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, "+OK") {
		return "", fmt.Errorf("%s", strings.TrimSpace(strings.TrimPrefix(line, "-ERR")))
	}
	return line, nil
}

func (p *minimalPOP3) login(user, password string) error {
	if _, err := p.command("USER %s", user); err != nil {
		return fmt.Errorf("POP3 user: %w", err)
	}
	if _, err := p.command("PASS %s", password); err != nil {
		return fmt.Errorf("POP3 password: %w", err)
	}
	return nil
}

func (p *minimalPOP3) list() ([]int, error) {
	if _, err := p.command("LIST"); err != nil {
		return nil, err
	}
	indexes := []int{}
	for {
		line, err := p.text.ReadLine()
		if err != nil {
			return indexes, err
		}
		if line == "." {
			return indexes, nil
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 {
			if index, err := strconv.Atoi(fields[0]); err == nil && index > 0 {
				indexes = append(indexes, index)
			}
		}
	}
}

func (p *minimalPOP3) retrieve(index int) ([]byte, error) {
	if _, err := p.command("RETR %d", index); err != nil {
		return nil, err
	}
	var buffer strings.Builder
	for {
		line, err := p.text.ReadLine()
		if err != nil {
			return nil, err
		}
		if line == "." {
			break
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		buffer.WriteString(line)
		buffer.WriteString("\r\n")
	}
	return []byte(buffer.String()), nil
}

func validatePOP3Login(host string, port int, security, user, password string) error {
	client, err := dialPOP3(host, port, security)
	if err != nil {
		return err
	}
	defer client.close()
	return client.login(user, password)
}

func readPOP3Message(reader io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, 64<<20))
}
