package httpapi

import "testing"

func TestMailLabelValidation(t *testing.T) {
	if name, ok := cleanLabelName("  Invoices  "); !ok || name != "Invoices" {
		t.Fatalf("valid label name rejected: %q %v", name, ok)
	}
	if _, ok := cleanLabelName("   "); ok {
		t.Fatal("empty label name accepted")
	}
	if _, ok := cleanLabelName(string(make([]rune, 41))); ok {
		t.Fatal("long label name accepted")
	}
	if color, ok := cleanLabelColor("#0F6CBD"); !ok || color != "#0f6cbd" {
		t.Fatalf("valid colour rejected: %q %v", color, ok)
	}
	if _, ok := cleanLabelColor("red"); ok {
		t.Fatal("non-hex colour accepted")
	}
	if color, ok := cleanLabelColor(""); !ok || color != "#0f6cbd" {
		t.Fatalf("default colour = %q %v", color, ok)
	}
}
