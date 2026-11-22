package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOfficeAIPromptIncludesDocumentContext(t *testing.T) {
	system, user, _, _ := officeAIPrompt("ask", "Plan.docx", "The budget is 500.", "What is the budget?", "English")
	combined := system + "\n" + user
	for _, want := range []string{
		"Question: What is the budget?",
		"Document: Plan.docx",
		"The budget is 500.",
		"Answer only from the document",
	} {
		if !strings.Contains(combined, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, combined)
		}
	}
}

func TestOfficeAIPromptTranslateUsesLanguage(t *testing.T) {
	_, user, _, _ := officeAIPrompt("translate", "", "Hello", "", "Spanish")
	if !strings.Contains(user, "into Spanish") {
		t.Fatalf("translation prompt = %q", user)
	}
}

func TestOfficeAIPromptActionItemsDoesNotRequirePrompt(t *testing.T) {
	_, user, _, _ := officeAIPrompt("action_items", "Notes.docx", "Avery will send the report by Friday.", "", "English")
	if !strings.Contains(user, "action items") || !strings.Contains(user, "Avery will send the report by Friday.") {
		t.Fatalf("action item prompt = %q", user)
	}
}

func TestOfficeAIFallbackRejectsOtherOfficePaths(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/office/not-ai", nil)
	rec := httptest.NewRecorder()
	(&Server{}).officeAIFallback(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
