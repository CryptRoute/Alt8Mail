package httpapi

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/meridian-mail/meridian/backend/internal/config"
)

func fakeClamd(t *testing.T, response string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		header := make([]byte, len("zINSTREAM\x00"))
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		for {
			var length [4]byte
			if _, err := io.ReadFull(conn, length[:]); err != nil {
				return
			}
			size := binary.BigEndian.Uint32(length[:])
			if size == 0 {
				break
			}
			if _, err := io.CopyN(io.Discard, conn, int64(size)); err != nil {
				return
			}
		}
		_, _ = io.WriteString(conn, response)
	}()
	return listener.Addr().String(), func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func TestScanUploadClamAV(t *testing.T) {
	address, closeServer := fakeClamd(t, "stream: OK\x00")
	defer closeServer()
	server := &Server{cfg: config.Config{ClamAVAddr: address}}
	if err := server.scanUpload(context.Background(), []byte("clean")); err != nil {
		t.Fatalf("clean scan failed: %v", err)
	}

	address, closeServer = fakeClamd(t, "stream: Eicar-Test-Signature FOUND\x00")
	defer closeServer()
	server = &Server{cfg: config.Config{ClamAVAddr: address}}
	err := server.scanUpload(context.Background(), []byte("virus"))
	if !errors.Is(err, errUploadInfected) {
		t.Fatalf("expected infected error, got %v", err)
	}
}

func TestScanUploadDisabledAndUnavailable(t *testing.T) {
	server := &Server{cfg: config.Config{}}
	if err := server.scanUpload(context.Background(), []byte("anything")); err != nil {
		t.Fatalf("empty address should disable scanning: %v", err)
	}
	server = &Server{cfg: config.Config{ClamAVAddr: "127.0.0.1:1"}}
	if err := server.scanUpload(context.Background(), []byte("anything")); err == nil || errors.Is(err, errUploadInfected) {
		t.Fatalf("unreachable scanner must fail closed with a transport error, got %v", err)
	}
}
