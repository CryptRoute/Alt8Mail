package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/CryptRoute/Alt8Mail/backend/internal/config"
)

func TestImportQuarantineFolderIsNotMappedToWebmail(t *testing.T) {
	if folder, ok := mapIMAPFolder(importQuarantineMailbox); ok {
		t.Fatalf("quarantine mailbox must not map to an app folder, got %q", folder)
	}
}

func TestScanImportedMessageDisabled(t *testing.T) {
	server := &Server{cfg: config.Config{ImportScan: false, ClamAVAddr: "127.0.0.1:1"}}
	quarantined, err := server.scanImportedMessage(context.Background(), nil, []byte("anything"), nil, time.Now(), "source@example.test", "<m@example.test>")
	if err != nil || quarantined {
		t.Fatalf("disabled import scanning should be a no-op, got quarantined=%v err=%v", quarantined, err)
	}
}

func TestScanImportedMessageCleanAndUnavailable(t *testing.T) {
	address, closeServer := fakeClamd(t, "stream: OK\x00")
	defer closeServer()
	server := &Server{cfg: config.Config{ImportScan: true, ClamAVAddr: address}}
	quarantined, err := server.scanImportedMessage(context.Background(), nil, []byte("clean"), nil, time.Now(), "source@example.test", "<m@example.test>")
	if err != nil || quarantined {
		t.Fatalf("clean import scan failed: quarantined=%v err=%v", quarantined, err)
	}

	server = &Server{cfg: config.Config{ImportScan: true, ClamAVAddr: "127.0.0.1:1"}}
	if _, err := server.scanImportedMessage(context.Background(), nil, []byte("clean"), nil, time.Now(), "source@example.test", "<m@example.test>"); err == nil {
		t.Fatal("an unavailable scanner must fail the import message closed")
	}

	server = &Server{cfg: config.Config{ImportScan: true, ImportScanMaxMB: 1, ClamAVAddr: "127.0.0.1:1"}}
	large := make([]byte, 2<<20)
	quarantined, err = server.scanImportedMessage(context.Background(), nil, large, nil, time.Now(), "source@example.test", "<large@example.test>")
	if err != nil || quarantined {
		t.Fatalf("oversized message should bypass the scanner, got quarantined=%v err=%v", quarantined, err)
	}
}
