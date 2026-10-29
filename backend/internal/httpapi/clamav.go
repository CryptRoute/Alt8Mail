package httpapi

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

var errUploadInfected = errors.New("upload contains malware")

const maxClamAVResponse = 4096

// scanUpload scans bytes through clamd's INSTREAM protocol. An empty
// ALT8MAIL_CLAMAV_ADDR disables scanning (local development). When configured,
// scanner errors fail closed so an unavailable scanner cannot be used to bypass
// malware detection on user uploads and guest-share replacements.
func (s *Server) scanUpload(ctx context.Context, data []byte) error {
	address := strings.TrimSpace(s.cfg.ClamAVAddr)
	if address == "" {
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	network := "unix"
	if !strings.HasPrefix(address, "/") {
		network = "tcp"
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return fmt.Errorf("connect to clamd: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	if _, err := io.WriteString(conn, "zINSTREAM\x00"); err != nil {
		return fmt.Errorf("start clamd scan: %w", err)
	}
	for offset := 0; offset < len(data); offset += 64 * 1024 {
		end := offset + 64*1024
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(chunk)))
		if _, err := conn.Write(length[:]); err != nil {
			return fmt.Errorf("write clamd scan length: %w", err)
		}
		if _, err := conn.Write(chunk); err != nil {
			return fmt.Errorf("write clamd scan chunk: %w", err)
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return fmt.Errorf("finish clamd scan: %w", err)
	}
	response, err := io.ReadAll(io.LimitReader(conn, maxClamAVResponse))
	if err != nil {
		return fmt.Errorf("read clamd scan result: %w", err)
	}
	result := strings.TrimSpace(strings.TrimRight(string(response), "\x00\r\n"))
	if strings.HasSuffix(strings.ToUpper(result), " OK") || strings.EqualFold(result, "OK") {
		return nil
	}
	if strings.Contains(strings.ToUpper(result), "FOUND") {
		return fmt.Errorf("%w: %s", errUploadInfected, result)
	}
	return fmt.Errorf("clamd scan was inconclusive: %s", result)
}
