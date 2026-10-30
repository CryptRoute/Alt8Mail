package httpapi

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var workTimePattern = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
var preferenceLanguages = map[string]bool{"en-US": true, "en-GB": true, "de-DE": true, "fr-FR": true, "es-ES": true, "pt-BR": true, "nl-NL": true, "it-IT": true, "ar": true}

func (s *Server) getSieve(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	writeJSON(w, http.StatusOK, publicSieve(state, sess.User.Email))
}

func (s *Server) getSieveScript(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	script := stateString(stateMap(state, "sieve"), "script")
	if script == "" {
		script = renderSieve(stateMap(state, "sieve"), sess.User.Email)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

func (s *Server) saveSieve(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var body map[string]any
	if !s.decodeBody(w, r, &body, 512*1024) {
		return
	}
	var response map[string]any
	err := s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		rules, vacation, errors := normalizeSieve(body, []string{"inbox", "archive", "spam", "trash"})
		if len(errors) > 0 {
			return failDetails(http.StatusBadRequest, "SIEVE_VALIDATION", "Sieve rules need attention", map[string]any{"errors": errors})
		}
		policy := s.tenantPolicyFor(r.Context(), sess.User.TenantID)
		if errors := validateSieveForwarding(rules, sess.User.Email, s.tenantMailDomains(r.Context(), sess.User.TenantID), policy.Mail); len(errors) > 0 {
			return failDetails(http.StatusForbidden, "FORWARDING_DISABLED", "Auto-forwarding is restricted by your organization", map[string]any{"errors": errors})
		}
		usedIDs := map[string]bool{}
		for index, raw := range rules {
			rule, _ := raw.(map[string]any)
			id := stateString(rule, "id")
			if usedIDs[id] {
				id = newID("rule")
				rule["id"] = id
			}
			usedIDs[id] = true
			rule["priority"] = index + 1
		}
		existing := stateMap(state, "sieve")
		if vacation == nil {
			if current, ok := existing["vacation"].(map[string]any); ok {
				vacation = current
			} else {
				vacation = defaultSieve()["vacation"].(map[string]any)
			}
		}
		forwarding := stateMap(existing, "forwarding")
		if len(forwarding) == 0 {
			forwarding = defaultForwarding()
		}
		if err := validateForwardingPolicy(forwarding, sess.User.Email, s.tenantMailDomains(r.Context(), sess.User.TenantID), policy.Mail); err != nil {
			return err
		}
		permittedFolders := map[string]bool{"inbox": true, "archive": true, "spam": true, "trash": true}
		for _, raw := range rules {
			rule, _ := raw.(map[string]any)
			action, _ := rule["action"].(map[string]any)
			if stateString(action, "type") == "move" && !permittedFolders[stateString(action, "value")] {
				return fail(http.StatusBadRequest, "SIEVE_VALIDATION", "Choose a valid destination folder")
			}
		}
		revision := stateInt(existing, "revision") + 1
		now := nowISO()
		next := map[string]any{
			"revision": revision, "updatedAt": now, "rules": rules, "vacation": vacation,
			"forwarding": forwarding,
		}
		script := renderSieve(next, sess.User.Email)
		if err := s.pushSieveScript(r.Context(), sess.User, script); err != nil {
			return failDetails(http.StatusBadGateway, "SIEVE_SERVICE", "Dovecot rejected the Sieve ruleset", map[string]any{"error": err.Error()})
		}
		next["script"] = script
		state["sieve"] = next
		vacationNote := ""
		if stateBool(vacation, "enabled") {
			vacationNote = " · vacation responder enabled"
		}
		appendStateAudit(state, "rule.saved", fmt.Sprintf("%d Sieve rules", len(rules)), sess.User.Name,
			fmt.Sprintf("Ruleset saved · revision %d%s", revision, vacationNote), clientIP(r), sanitizeUserAgent(r.UserAgent()))
		response = publicSieve(state, sess.User.Email)
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	category := r.URL.Query().Get("category")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	entries := filterStateAudit(state, category)
	total := len(entries)
	end := offset + limit
	if end > total {
		end = total
	}
	paged := []any{}
	if offset < total {
		paged = entries[offset:end]
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "count": len(paged), "entries": paged})
}

func (s *Server) getAuditCSV(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	state, ok := s.loadStateFor(r.Context(), sess.User.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STATE_UNAVAILABLE", "This mailbox is not initialized yet")
		return
	}
	entries := filterStateAudit(state, r.URL.Query().Get("category"))
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	writer.UseCRLF = true
	_ = writer.Write([]string{"time", "action", "target", "actor", "detail", "ip"})
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		_ = writer.Write([]string{
			csvSafe(stateString(entry, "ts")), csvSafe(stateString(entry, "action")),
			csvSafe(stateString(entry, "target")), csvSafe(stateString(entry, "actor")),
			csvSafe(stateString(entry, "detail")), csvSafe(stateString(entry, "ip")),
		})
	}
	writer.Flush()
	filename := "a8m-audit-" + time.Now().UTC().Format("2006-01-02") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(builder.String()))
}

func csvSafe(value string) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

func (s *Server) patchPreferencesState(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	var patch map[string]any
	if !s.decodeBody(w, r, &patch, 128*1024) {
		return
	}
	var result map[string]any
	var encoded []byte
	err := s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		current := statePreferences(state, preferencesFor(sess.User))
		changed := []string{}
		assign := func(key string, value any) {
			left, _ := json.Marshal(current[key])
			right, _ := json.Marshal(value)
			if string(left) != string(right) {
				current[key] = value
				changed = append(changed, key)
			}
		}
		if value, ok := patch["language"]; ok {
			language := fmt.Sprint(value)
			if !preferenceLanguages[language] {
				return fail(http.StatusBadRequest, "PREFERENCES", "Unsupported interface language")
			}
			assign("language", language)
		}
		if value, ok := patch["timeFormat"]; ok {
			format := fmt.Sprint(value)
			if format != "12h" && format != "24h" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Time format must be 12h or 24h")
			}
			assign("timeFormat", format)
		}
		if value, ok := patch["dateFormat"]; ok {
			format := fmt.Sprint(value)
			if format != "auto" && format != "mdy" && format != "dmy" && format != "ymd" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Unsupported date format")
			}
			assign("dateFormat", format)
		}
		if value, ok := patch["timeZone"]; ok {
			zone := fmt.Sprint(value)
			if _, err := time.LoadLocation(zone); err != nil {
				return fail(http.StatusBadRequest, "PREFERENCES", "Unknown IANA time zone")
			}
			assign("timeZone", zone)
		}
		if value, ok := patch["weekStart"]; ok {
			start := fmt.Sprint(value)
			if start != "monday" && start != "sunday" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Week start must be monday or sunday")
			}
			assign("weekStart", start)
		}
		for _, key := range []string{"workStart", "workEnd"} {
			if value, ok := patch[key]; ok {
				text := fmt.Sprint(value)
				if !workTimePattern.MatchString(text) {
					return fail(http.StatusBadRequest, "PREFERENCES", "Work hours must use HH:MM")
				}
				assign(key, text)
			}
		}
		if fmt.Sprint(current["workStart"]) >= fmt.Sprint(current["workEnd"]) {
			return fail(http.StatusBadRequest, "PREFERENCES", "Work end must be after work start")
		}
		if value, ok := patch["workDays"]; ok {
			rawDays, ok := value.([]any)
			if !ok || len(rawDays) == 0 {
				return fail(http.StatusBadRequest, "PREFERENCES", "Work days must be ISO weekdays 1–7")
			}
			days := []any{}
			seen := map[int]bool{}
			for _, rawDay := range rawDays {
				number := int(stateInt(map[string]any{"day": rawDay}, "day"))
				if number < 1 || number > 7 {
					return fail(http.StatusBadRequest, "PREFERENCES", "Work days must be ISO weekdays 1–7")
				}
				if !seen[number] {
					seen[number] = true
					days = append(days, number)
				}
			}
			sortIntsAny(days)
			assign("workDays", days)
		}
		if value, ok := patch["defaultFrom"]; ok {
			email := strings.ToLower(strings.TrimSpace(fmt.Sprint(value)))
			if !s.identitySendable(r.Context(), sess.User.ID, email) {
				return fail(http.StatusBadRequest, "PREFERENCES", "Default From must be a verified sending identity")
			}
			assign("defaultFrom", email)
		}
		if value, ok := patch["signature"]; ok {
			text := fmt.Sprint(value)
			if len([]rune(text)) > 4000 {
				return fail(http.StatusBadRequest, "PREFERENCES", "Signature is too long")
			}
			assign("signature", text)
		}
		for _, key := range []string{"signatureEnabled", "saveCopyEnabled", "spellCheckEnabled", "autoAdvance", "markReadOnOpen", "defaultReceipt", "notifyDesktop", "notifySound", "notifyCalendar", "externalWarnings"} {
			if value, ok := patch[key]; ok {
				flag, valid := value.(bool)
				if !valid {
					return fail(http.StatusBadRequest, "PREFERENCES", key+" must be true or false")
				}
				assign(key, flag)
			}
		}
		if value, ok := patch["inboxView"]; ok {
			view := fmt.Sprint(value)
			if view != "grouped" && view != "individual" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Inbox view must be grouped or individual")
			}
			assign("inboxView", view)
		}
		if value, ok := patch["theme"]; ok {
			theme := fmt.Sprint(value)
			if theme != "light" && theme != "dark" && theme != "system" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Theme must be light, dark or system")
			}
			assign("theme", theme)
		}
		if value, ok := patch["accent"]; ok {
			accent := fmt.Sprint(value)
			if accent != "blue" && accent != "teal" && accent != "violet" && accent != "rose" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Unsupported accent color")
			}
			assign("accent", accent)
		}
		if value, ok := patch["density"]; ok {
			density := fmt.Sprint(value)
			if density != "comfortable" && density != "compact" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Density must be comfortable or compact")
			}
			assign("density", density)
		}
		if value, ok := patch["defaultPriority"]; ok {
			priority := fmt.Sprint(value)
			if priority != "low" && priority != "normal" && priority != "high" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Default priority must be low, normal or high")
			}
			assign("defaultPriority", priority)
		}
		if value, ok := patch["undoSendSeconds"]; ok {
			seconds := stateInt(map[string]any{"v": value}, "v")
			if seconds < 5 || seconds > 10 {
				return fail(http.StatusBadRequest, "PREFERENCES", "Undo send window must be between 5 and 10 seconds")
			}
			assign("undoSendSeconds", seconds)
		}
		if value, ok := patch["composeMode"]; ok {
			mode := fmt.Sprint(value)
			if mode != "html" && mode != "plain" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Compose mode must be html or plain")
			}
			assign("composeMode", mode)
		}
		if value, ok := patch["composeWidth"]; ok {
			width := fmt.Sprint(value)
			if width != "standard" && width != "wide" && width != "full" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Compose width must be standard, wide or full")
			}
			assign("composeWidth", width)
		}
		if value, ok := patch["composePosition"]; ok {
			position := fmt.Sprint(value)
			if position != "center" && position != "bottom-right" && position != "bottom-left" {
				return fail(http.StatusBadRequest, "PREFERENCES", "Compose position must be center, bottom-right or bottom-left")
			}
			assign("composePosition", position)
		}
		stateMap(state, "user")["preferences"] = current
		payload, err := json.Marshal(current)
		if err != nil {
			return err
		}
		encoded = payload
		appendStateAudit(state, "preferences.updated", "Workspace preferences", sess.User.Name,
			"Updated "+strings.Join(changed, ", "), clientIP(r), sanitizeUserAgent(r.UserAgent()))
		result = map[string]any{"preferences": current, "changed": changed}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	if len(encoded) > 0 {
		_, _ = s.store.DB.Exec(r.Context(), `UPDATE users SET preferences = $2, updated_at = now() WHERE id = $1`, sess.User.ID, encoded)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resetPreferencesState(w http.ResponseWriter, r *http.Request) {
	sess := s.requireSessionFromContext(r)
	defaults := preferencesFor(userRecord{})
	var result map[string]any
	err := s.withState(sess.User.ID, sess.User.TenantID, func(state map[string]any) error {
		ensureStateCollections(state)
		stateMap(state, "user")["preferences"] = defaults
		encoded, err := json.Marshal(defaults)
		if err != nil {
			return err
		}
		_, err = s.store.DB.Exec(r.Context(), `UPDATE users SET preferences = $2, updated_at = now() WHERE id = $1`, sess.User.ID, encoded)
		if err != nil {
			return err
		}
		appendStateAudit(state, "preferences.reset", "Workspace preferences", sess.User.Name,
			"Reset all personal preferences to defaults", clientIP(r), sanitizeUserAgent(r.UserAgent()))
		result = map[string]any{"preferences": defaults, "changed": []string{}}
		return nil
	})
	if err != nil {
		writeFromError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func sortIntsAny(values []any) {
	for i := 1; i < len(values); i++ {
		current := values[i]
		j := i - 1
		for j >= 0 && fmt.Sprint(values[j]) > fmt.Sprint(current) {
			values[j+1] = values[j]
			j--
		}
		values[j+1] = current
	}
}
