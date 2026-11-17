package httpapi

import (
	"net"
	"testing"
)

func TestURLImportRejectsPrivateAndLinkLocalAddresses(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.0.0.8", "172.16.4.2", "192.168.1.9", "169.254.169.254", "::1", "fd00::1", "fe80::1"}
	for _, raw := range blocked {
		if safeRemoteIP(net.ParseIP(raw)) {
			t.Fatalf("private address %s was accepted by URL import", raw)
		}
	}
	if !safeRemoteIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address was rejected by URL import")
	}
}
