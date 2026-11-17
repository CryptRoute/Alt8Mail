package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRspamdFilePreservesAdminRules(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.conf")
	target := filepath.Join(dir, "target.conf")
	desired := "ADMIN_RULE {\n  score = 1;\n}\n\n# Domains hosted by this Alt8Mail instance\nLOCAL_RULE {\n  score = 100;\n}\n"
	if err := os.WriteFile(source, []byte(desired), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("EXISTING_RULE {\n  score = 2;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRspamdFile(target, source, "ADMIN_RULE", "LOCAL_RULE", "# Domains hosted by this Alt8Mail instance"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(target)
	text := string(first)
	if !strings.Contains(text, "EXISTING_RULE") {
		t.Fatal("administrator rules were removed")
	}
	if strings.Count(text, "LOCAL_RULE") != 1 {
		t.Fatalf("local rule count = %d", strings.Count(text, "LOCAL_RULE"))
	}
	if !strings.Contains(text, "score = 100") {
		t.Fatal("local rule block was not appended")
	}
	if err := ensureRspamdFile(target, source, "ADMIN_RULE", "LOCAL_RULE", "# Domains hosted by this Alt8Mail instance"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(target)
	if string(second) != text {
		t.Fatal("a repeated run duplicated or changed the rule")
	}
}

func TestEnsureRspamdFileCreatesMissingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.conf")
	target := filepath.Join(dir, "nested", "target.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("RULE {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRspamdFile(target, source, "", "RULE", ""); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "RULE {}\n" {
		t.Fatalf("target content = %q, err = %v", content, err)
	}
}
