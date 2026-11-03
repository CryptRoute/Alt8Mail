package threatintel

import (
	"reflect"
	"testing"
)

func hostNames(hosts []URLHost) []string {
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, host.Host)
	}
	return out
}

func TestExtractURLHostsNormalizesAndDeduplicates(t *testing.T) {
	htmlBody := `<p>Visit <a href="https://Example.COM./path?x=1">https://example.com/page</a>.</p>
		<img src="http://Bit.Ly/abc">
		<p>Also HTTPS://Sub.Example.org/a?b=1.</p>
		<a href="mailto:help@example.com">mail</a>
		<a href="http://localhost/admin">local</a>`
	textBody := `Backup: https://example.com/again and http://127.0.0.1/private`
	hosts := ExtractURLHosts(htmlBody, textBody)
	want := []string{"bit.ly", "example.com", "sub.example.org"}
	if got := hostNames(hosts); !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %#v, want %#v", got, want)
	}
	if !hosts[0].Shared {
		t.Fatal("bit.ly should be marked as a shared destination")
	}
	if hosts[1].Shared || hosts[2].Shared {
		t.Fatal("ordinary hosts must not be marked shared")
	}
}

func TestExtractURLHostsHandlesEntitiesAndIDN(t *testing.T) {
	htmlBody := `<a href="https://bücher.example/a?x=1&amp;y=2">book</a>
		<a href="https://xn--bcher-kva.example/other">book</a>`
	hosts := ExtractURLHosts(htmlBody, "")
	want := []string{"xn--bcher-kva.example"}
	if got := hostNames(hosts); !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %#v, want %#v", got, want)
	}
}

func TestExtractURLHostsSkipsUnsupportedAndLocalTargets(t *testing.T) {
	text := `ftp://files.example/a javascript:alert(1) mailto:a@example.com
		http://localhost/a http://internal/b http://foo.test/c
		http://127.0.0.1/d http://10.10.10.10/e https://valid.example/f`
	hosts := ExtractURLHosts("", text)
	want := []string{"valid.example"}
	if got := hostNames(hosts); !reflect.DeepEqual(got, want) {
		t.Fatalf("hosts = %#v, want %#v", got, want)
	}
}

func TestExtractURLHostsLimitsAndSorts(t *testing.T) {
	body := ""
	for i := 0; i < MaxURLHosts+20; i++ {
		body += " https://host" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".example/path"
	}
	hosts := ExtractURLHosts("", body)
	if len(hosts) > MaxURLHosts {
		t.Fatalf("got %d hosts, want at most %d", len(hosts), MaxURLHosts)
	}
	for i := 1; i < len(hosts); i++ {
		if hosts[i-1].Host >= hosts[i].Host {
			t.Fatalf("hosts are not sorted and unique at %d: %q >= %q", i, hosts[i-1].Host, hosts[i].Host)
		}
	}
}

func TestSharedDestinationCatalog(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"bit.ly", true},
		{"foo.bit.ly", true},
		{"notbit.ly", false},
		{"bit.ly.evil.example", false},
		{"drive.google.com", true},
		{"click.mailchimp.com", true},
		{"u123.sendgrid.net", true},
		{"notsendgrid.net", false},
		{"example.com", false},
	}
	for _, item := range cases {
		if got := IsSharedDestination(item.host); got != item.want {
			t.Fatalf("IsSharedDestination(%q) = %v, want %v", item.host, got, item.want)
		}
	}
}

func TestActiveSetDeduplicatesIndicators(t *testing.T) {
	set := ActiveSet([]ReportIndicators{
		{Sender: " Alice@Example.com. ", Domain: "Example.com.", URLHosts: []string{"Example.com.", "a.example"}},
		{Sender: "alice@example.com", Domain: "example.com", URLHosts: []string{"a.example", "b.example"}},
	})
	if !reflect.DeepEqual(set.Senders, []string{"alice@example.com"}) {
		t.Fatalf("senders = %#v", set.Senders)
	}
	if !reflect.DeepEqual(set.Domains, []string{"example.com"}) {
		t.Fatalf("domains = %#v", set.Domains)
	}
	if !reflect.DeepEqual(set.URLs, []string{"a.example", "b.example", "example.com"}) {
		t.Fatalf("urls = %#v", set.URLs)
	}
}
