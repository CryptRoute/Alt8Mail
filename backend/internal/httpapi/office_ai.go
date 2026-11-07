package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

const officeAIMaxText = 16000

type officeAIRequest struct {
	Action   string `json:"action"`
	Text     string `json:"text"`
	Prompt   string `json:"prompt"`
	Language string `json:"language"`
}

func (s *Server) officeAIEnabled(ctx context.Context, user userRecord) bool {
	policy, allowed := s.officeUserAllowed(ctx, user)
	if !allowed || !policy.AI {
		return false
	}
	settings, err := s.aiSettingsFor(ctx, user.TenantID)
	return err == nil && settings.Enabled && strings.TrimSpace(settings.Model) != ""
}

func (s *Server) officeAI(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	policy, allowed := s.officeUserAllowed(r.Context(), sess.User)
	if !allowed || !policy.AI {
		writeError(w, http.StatusForbidden, "OFFICE_AI_POLICY", "Office AI is disabled for your organization or account")
		return
	}
	var body officeAIRequest
	if !s.decodeBody(w, r, &body, 64<<10) {
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	text := strings.TrimSpace(body.Text)
	prompt := strings.TrimSpace(body.Prompt)
	language := strings.TrimSpace(body.Language)
	if utf8.RuneCountInString(text) > officeAIMaxText {
		writeError(w, http.StatusRequestEntityTooLarge, "OFFICE_AI_TEXT", "Select a shorter passage for the AI assistant")
		return
	}
	if utf8.RuneCountInString(prompt) > 2000 {
		writeError(w, http.StatusBadRequest, "OFFICE_AI_PROMPT", "Keep the AI instruction under 2000 characters")
		return
	}
	switch action {
	case "summarize", "rewrite", "translate", "continue", "custom":
	default:
		writeError(w, http.StatusBadRequest, "OFFICE_AI_ACTION", "Choose a supported AI action")
		return
	}
	if action != "custom" && action != "continue" && text == "" {
		writeError(w, http.StatusBadRequest, "OFFICE_AI_TEXT", "Select text in the document first")
		return
	}
	if action == "continue" && text == "" && prompt == "" {
		writeError(w, http.StatusBadRequest, "OFFICE_AI_TEXT", "Select text or describe what to continue")
		return
	}
	if action == "custom" && prompt == "" {
		writeError(w, http.StatusBadRequest, "OFFICE_AI_PROMPT", "Describe what the assistant should write")
		return
	}
	if action == "translate" && language == "" {
		writeError(w, http.StatusBadRequest, "OFFICE_AI_LANGUAGE", "Choose a target language")
		return
	}
	settings, err := s.aiReady(r.Context(), sess.User.TenantID)
	if err != nil {
		writeFromError(w, err)
		return
	}
	system, user, err := officeAIPrompt(action, text, prompt, language)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OFFICE_AI", err.Error())
		return
	}
	result, err := s.callAI(r.Context(), settings, system, user, 1600, 0.4)
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI_UPSTREAM", "The AI assistant could not complete the request")
		return
	}
	result = strings.TrimSpace(result)
	if result == "" {
		writeError(w, http.StatusBadGateway, "AI_EMPTY", "The AI assistant returned an empty response")
		return
	}
	s.recordWorkspaceAudit(r.Context(), sess, "office.ai_used", action,
		fmt.Sprintf("Office AI · %d selected characters", utf8.RuneCountInString(text)))
	writeJSON(w, http.StatusOK, map[string]any{"text": result})
}

func officeAIPrompt(action, text, prompt, language string) (string, string, error) {
	system := "You are Alt8Mail's writing assistant inside a secure document editor. " +
		"Follow the user's instruction precisely. Return plain text only: no Markdown fences, no preamble, no commentary, and no repetition of the instruction. " +
		"Preserve names, numbers, dates and factual details."
	instruction := ""
	switch action {
	case "summarize":
		instruction = "Summarize the selected document text in a concise paragraph or at most five short bullet lines. Keep the key facts and decisions."
	case "rewrite":
		instruction = "Rewrite the selected text so it is clear, concise and professional. Preserve its meaning and facts. Return only the rewritten text."
	case "translate":
		language = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(language, "\r", " "), "\n", " "))
		if utf8.RuneCountInString(language) > 40 || language == "" {
			return "", "", fmt.Errorf("choose a valid target language")
		}
		instruction = "Translate the selected text into " + language + ". Preserve names, numbers and tone. Return only the translation."
	case "continue":
		instruction = "Continue the selected text naturally in the same tone, style and language. Return only the continuation, not the original text."
	case "custom":
		instruction = "Follow this instruction: " + prompt
	default:
		return "", "", fmt.Errorf("unsupported AI action")
	}
	user := instruction
	if text != "" {
		user += "\n\nSelected text:\n\"\"\"\n" + text + "\n\"\"\""
	}
	if prompt != "" && action != "custom" {
		if text == "" && action == "continue" {
			user += "\n\nContext or instruction:\n" + prompt
		} else {
			user += "\n\nAdditional instruction:\n" + prompt
		}
	}
	return system, user, nil
}
