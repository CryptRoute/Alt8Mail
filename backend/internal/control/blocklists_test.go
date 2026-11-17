package control

import "testing"

func TestBlocklistResponseBlocked(t *testing.T) {
	for _, value := range []string{"127.255.255.254", "127.255.255.2,127.255.255.254", " 127.255.255.255 "} {
		if !blocklistResponseBlocked(value) {
			t.Fatalf("expected %q to be treated as a resolver-policy reply", value)
		}
	}
	for _, value := range []string{"", "127.0.0.2", "127.0.0.4", "192.0.2.1", "NXDOMAIN", "no such host"} {
		if blocklistResponseBlocked(value) {
			t.Fatalf("expected %q not to be treated as a resolver-policy reply", value)
		}
	}
}

func TestBlocklistKey(t *testing.T) {
	if got := dnsblKey([]byte{130, 94, 46, 22}); got != "22.46.94.130" {
		t.Fatalf("unexpected IPv4 DNSBL key %q", got)
	}
}
