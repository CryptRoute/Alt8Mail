package httpapi

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var sieveFields = map[string]bool{"from": true, "to": true, "subject": true, "body": true, "size": true}
var sieveOperators = map[string]bool{"contains": true, "is": true, "matches": true, "notcontains": true, "over": true, "under": true}
var sieveActions = map[string]bool{"move": true, "markRead": true, "star": true, "discard": true, "redirect": true, "keep": true}
var sieveFolders = map[string]bool{"inbox": true, "archive": true, "spam": true, "trash": true}
var sieveIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)
var sieveDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var sieveEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var sieveSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)$`)

// sieveComment keeps user-controlled labels from breaking out of a generated
// comment with a newline and injecting Sieve statements. Values are quoted
// separately by sieveQuote.
func sieveComment(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func sieveQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return `"` + value + `"`
}

func normalizeSieve(input map[string]any, folders []string) ([]any, map[string]any, []string) {
	errors := []string{}
	allowedFolders := map[string]bool{}
	for _, folder := range folders {
		allowedFolders[folder] = true
	}
	rulesRaw, _ := input["rules"].([]any)
	if len(rulesRaw) > 50 {
		errors = append(errors, "A maximum of 50 rules is allowed")
	}
	names := map[string]bool{}
	rules := []any{}
	for index, raw := range rulesRaw {
		label := fmt.Sprintf("Rule %d", index+1)
		rule, _ := raw.(map[string]any)
		if rule == nil {
			errors = append(errors, label+": invalid rule")
			continue
		}
		name := strings.TrimSpace(stateString(rule, "name"))
		if strings.IndexByte(name, 13) >= 0 || strings.IndexByte(name, 10) >= 0 {
			errors = append(errors, label+": name cannot contain line breaks")
		}
		if name == "" {
			errors = append(errors, label+": a name is required")
		} else if len([]rune(name)) > 80 {
			errors = append(errors, label+": name is too long")
		} else if names[strings.ToLower(name)] {
			errors = append(errors, label+": duplicate name \""+name+"\"")
		}
		names[strings.ToLower(name)] = true
		match := "all"
		if stateString(rule, "match") == "any" {
			match = "any"
		}
		conditionsRaw, _ := rule["conditions"].([]any)
		if len(conditionsRaw) == 0 {
			errors = append(errors, label+": at least one condition is required")
		}
		if len(conditionsRaw) > 10 {
			errors = append(errors, label+": a maximum of 10 conditions is allowed")
		}
		conditions := []any{}
		for conditionIndex, conditionRaw := range conditionsRaw {
			condition, _ := conditionRaw.(map[string]any)
			if condition == nil {
				errors = append(errors, fmt.Sprintf("%s: condition %d is invalid", label, conditionIndex+1))
				continue
			}
			field := stateString(condition, "field")
			operator := stateString(condition, "operator")
			value := strings.TrimSpace(stateString(condition, "value"))
			if !sieveFields[field] {
				errors = append(errors, fmt.Sprintf("%s: condition %d has an unknown field", label, conditionIndex+1))
			}
			if !sieveOperators[operator] {
				errors = append(errors, fmt.Sprintf("%s: condition %d has an unknown operator", label, conditionIndex+1))
			}
			if field == "size" {
				if operator != "over" && operator != "under" {
					errors = append(errors, label+": size conditions use over or under")
				}
				if !sieveSizePattern.MatchString(value) || value == "0" {
					errors = append(errors, label+": size must be a positive number of KB")
				} else {
					conditions = append(conditions, map[string]any{"field": field, "operator": operator, "value": value})
					continue
				}
				conditions = append(conditions, map[string]any{"field": field, "operator": operator, "value": 0})
			} else if value == "" {
				errors = append(errors, fmt.Sprintf("%s: condition %d needs a value", label, conditionIndex+1))
			} else if len([]rune(value)) > 200 {
				errors = append(errors, label+": condition value is too long")
			}
			conditions = append(conditions, map[string]any{"field": field, "operator": operator, "value": value})
		}
		actionRaw, _ := rule["action"].(map[string]any)
		actionType := stateString(actionRaw, "type")
		if !sieveActions[actionType] {
			errors = append(errors, label+": choose an action")
			actionType = ""
		}
		actionValue := strings.TrimSpace(stateString(actionRaw, "value"))
		if actionType == "move" {
			if !allowedFolders[actionValue] {
				errors = append(errors, label+": choose a valid destination folder")
			}
		} else if actionType == "redirect" {
			if !sieveEmailPattern.MatchString(actionValue) {
				errors = append(errors, label+": redirect needs a valid email address")
			}
		} else {
			actionValue = ""
		}
		id := stateString(rule, "id")
		if !sieveIDPattern.MatchString(id) {
			id = fmt.Sprintf("rule_%d", index+1)
		}
		priority := index + 1
		if value := stateInt(rule, "priority"); value > 0 {
			priority = int(value)
		}
		rules = append(rules, map[string]any{
			"id": id, "name": name, "enabled": rule["enabled"] != false,
			"priority": priority, "match": match, "conditions": conditions,
			"action": map[string]any{"type": actionType, "value": actionValue},
		})
	}

	var vacation map[string]any
	if rawVacation, ok := input["vacation"].(map[string]any); ok {
		days := stateInt(rawVacation, "days")
		if days < 1 || days > 30 {
			days = 7
		}
		start := strings.TrimSpace(stateString(rawVacation, "start"))
		end := strings.TrimSpace(stateString(rawVacation, "end"))
		internalRaw, _ := rawVacation["internal"].(map[string]any)
		if internalRaw == nil {
			internalRaw = map[string]any{"subject": rawVacation["subject"], "message": rawVacation["message"]}
		}
		externalRaw, _ := rawVacation["external"].(map[string]any)
		if externalRaw == nil {
			externalRaw = map[string]any{"enabled": false}
		}
		internal := map[string]any{
			"subject": truncate(firstNonEmpty(stateString(internalRaw, "subject"), "Out of office"), 200),
			"message": strings.TrimSpace(stateString(internalRaw, "message")),
		}
		external := map[string]any{
			"enabled": stateBool(externalRaw, "enabled"),
			"subject": truncate(firstNonEmpty(stateString(externalRaw, "subject"), "Away from the office"), 200),
			"message": strings.TrimSpace(stateString(externalRaw, "message")),
		}
		enabled := stateBool(rawVacation, "enabled")
		if enabled && internal["message"] == "" {
			errors = append(errors, "Automatic replies: an inside-organization message is required")
		}
		if enabled && stateBool(external, "enabled") && external["message"] == "" {
			errors = append(errors, "Automatic replies: an outside-organization message is required")
		}
		if len([]rune(fmt.Sprint(internal["message"]))) > 4000 || len([]rune(fmt.Sprint(external["message"]))) > 4000 {
			errors = append(errors, "Automatic replies: message is too long")
		}
		if start != "" && !sieveDatePattern.MatchString(start) {
			errors = append(errors, "Automatic replies: start date is invalid")
		}
		if end != "" && !sieveDatePattern.MatchString(end) {
			errors = append(errors, "Automatic replies: end date is invalid")
		}
		if start != "" && end != "" && end < start {
			errors = append(errors, "Automatic replies: end date must be after the start date")
		}
		vacation = map[string]any{"enabled": enabled, "start": start, "end": end, "days": days, "internal": internal, "external": external}
	}
	return rules, vacation, errors
}

func renderSieve(sieve map[string]any, email string) string {
	rulesRaw, _ := sieve["rules"].([]any)
	requires := map[string]bool{}
	for _, raw := range rulesRaw {
		rule, _ := raw.(map[string]any)
		if rule == nil || rule["enabled"] == false {
			continue
		}
		action, _ := rule["action"].(map[string]any)
		if stateString(action, "type") == "move" {
			requires["fileinto"] = true
		}
		if stateString(action, "type") == "markRead" || stateString(action, "type") == "star" {
			requires["imap4flags"] = true
		}
		if conditions, ok := rule["conditions"].([]any); ok {
			for _, rawCondition := range conditions {
				condition, _ := rawCondition.(map[string]any)
				if condition != nil && stateString(condition, "field") == "body" {
					requires["body"] = true
				}
			}
		}
	}
	vacation, _ := sieve["vacation"].(map[string]any)
	if stateBool(vacation, "enabled") {
		requires["vacation"] = true
		if stateString(vacation, "start") != "" || stateString(vacation, "end") != "" {
			requires["date"] = true
			requires["relational"] = true
		}
	}
	forwarding, _ := sieve["forwarding"].(map[string]any)
	if stateBool(forwarding, "enabled") && forwarding["permanent"] == false {
		requires["date"] = true
		requires["relational"] = true
	}
	requiresList := []string{}
	for name := range requires {
		requiresList = append(requiresList, name)
	}
	sort.Strings(requiresList)
	lines := []string{
		"# Generated by Alt8Mail — manage this ruleset in the webmail.",
		fmt.Sprintf("# Owner: %s · revision %d", sieveComment(email), stateInt(sieve, "revision")),
		fmt.Sprintf("# Updated: %s", sieveComment(stateString(sieve, "updatedAt"))),
		"",
	}
	if len(requiresList) > 0 {
		quoted := []string{}
		for _, name := range requiresList {
			quoted = append(quoted, sieveQuote(name))
		}
		lines = append(lines, "require ["+strings.Join(quoted, ", ")+"];")
		lines = append(lines, "")
	}

	if forwarding, ok := sieve["forwarding"].(map[string]any); ok {
		lines = append(lines, renderForwarding(forwarding)...)
		if stateBool(forwarding, "enabled") {
			lines = append(lines, "")
		}
	}

	ordered := append([]any{}, rulesRaw...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, _ := ordered[i].(map[string]any)
		right, _ := ordered[j].(map[string]any)
		return stateInt(left, "priority") < stateInt(right, "priority")
	})
	for _, raw := range ordered {
		rule, _ := raw.(map[string]any)
		if rule == nil {
			continue
		}
		if rule["enabled"] == false {
			lines = append(lines, "# [disabled] "+sieveComment(stateString(rule, "name")))
			continue
		}
		lines = append(lines, "# "+sieveComment(stateString(rule, "name")))
		conditions, _ := rule["conditions"].([]any)
		tests := []string{}
		for _, rawCondition := range conditions {
			condition, _ := rawCondition.(map[string]any)
			if condition != nil {
				tests = append(tests, sieveConditionTest(condition))
			}
		}
		test := ""
		if len(tests) == 1 {
			test = tests[0]
		} else {
			operator := "allof"
			if stateString(rule, "match") == "any" {
				operator = "anyof"
			}
			test = operator + " (" + strings.Join(tests, ", ") + ")"
		}
		lines = append(lines, "if "+test+" {")
		for _, statement := range sieveActionStatements(rule) {
			lines = append(lines, "  "+statement)
		}
		lines = append(lines, "}", "")
	}

	if stateBool(vacation, "enabled") {
		internal, _ := vacation["internal"].(map[string]any)
		external, _ := vacation["external"].(map[string]any)
		dateTests := []string{}
		if start := stateString(vacation, "start"); start != "" {
			dateTests = append(dateTests, `currentdate :value "ge" "date" `+sieveQuote(start))
		}
		if end := stateString(vacation, "end"); end != "" {
			dateTests = append(dateTests, `currentdate :value "le" "date" `+sieveQuote(end))
		}
		vacationAction := func(subject, message string) string {
			return fmt.Sprintf("vacation :days %d :subject %s %s;", stateInt(vacation, "days"), sieveQuote(subject), sieveQuote(message))
		}
		domain := ""
		if index := strings.LastIndex(email, "@"); index >= 0 {
			domain = email[index+1:]
		}
		if stateBool(external, "enabled") && domain != "" {
			lines = append(lines, "# Automatic replies · inside the organization")
			inside := append(append([]string{}, dateTests...), `address :domain :is "from" `+sieveQuote(domain))
			lines = append(lines, "if "+joinSieveTests(inside)+" {")
			lines = append(lines, "  "+vacationAction(firstNonEmpty(stateString(internal, "subject"), "Out of office"), stateString(internal, "message")))
			lines = append(lines, "}", "")
			lines = append(lines, "# Automatic replies · outside the organization")
			outside := append(append([]string{}, dateTests...), `not address :domain :is "from" `+sieveQuote(domain))
			lines = append(lines, "if "+joinSieveTests(outside)+" {")
			lines = append(lines, "  "+vacationAction(firstNonEmpty(stateString(external, "subject"), "Away from the office"), stateString(external, "message")))
			lines = append(lines, "}", "")
		} else {
			lines = append(lines, "# Automatic reply")
			if len(dateTests) > 0 {
				lines = append(lines, "if "+joinSieveTests(dateTests)+" {")
				lines = append(lines, "  "+vacationAction(firstNonEmpty(stateString(internal, "subject"), "Out of office"), stateString(internal, "message")))
				lines = append(lines, "}")
			} else {
				lines = append(lines, vacationAction(firstNonEmpty(stateString(internal, "subject"), "Out of office"), stateString(internal, "message")))
			}
			lines = append(lines, "")
		}
	}
	lines = append(lines, "# End of ruleset")
	return strings.Join(lines, "\n") + "\n"
}

func joinSieveTests(tests []string) string {
	if len(tests) == 1 {
		return tests[0]
	}
	return "allof (" + strings.Join(tests, ", ") + ")"
}

func sieveConditionTest(condition map[string]any) string {
	field := stateString(condition, "field")
	operator := stateString(condition, "operator")
	value := stateString(condition, "value")
	if field == "size" {
		kind := "over"
		if operator == "under" {
			kind = "under"
		}
		return fmt.Sprintf("size :%s %sK", kind, value)
	}
	op := ":contains"
	if operator == "is" {
		op = ":is"
	} else if operator == "matches" {
		op = ":matches"
	}
	var test string
	if field == "from" || field == "to" {
		test = "address " + op + " " + sieveQuote(field) + " " + sieveQuote(value)
	} else if field == "body" {
		test = "body " + op + " " + sieveQuote(value)
	} else {
		test = "header " + op + " " + sieveQuote(field) + " " + sieveQuote(value)
	}
	if operator == "notcontains" {
		return "not " + test
	}
	return test
}

func sieveActionStatements(rule map[string]any) []string {
	action, _ := rule["action"].(map[string]any)
	value := sieveFolderName(stateString(action, "value"))
	switch stateString(action, "type") {
	case "move":
		return []string{"fileinto " + sieveQuote(value) + ";", "stop;"}
	case "markRead":
		return []string{`addflag "\\Seen";`}
	case "star":
		return []string{`addflag "\\Flagged";`}
	case "discard":
		return []string{"discard;", "stop;"}
	case "redirect":
		return []string{"redirect " + sieveQuote(value) + ";", "stop;"}
	default:
		return []string{"keep;", "stop;"}
	}
}

func sieveFolderName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "inbox":
		return "INBOX"
	case "archive":
		return "Archive"
	case "spam", "junk":
		return "Junk"
	case "trash":
		return "Trash"
	case "sent":
		return "Sent"
	case "drafts":
		return "Drafts"
	}
	return value
}

func publicSieve(state map[string]any, email string) map[string]any {
	sieve := stateMap(state, "sieve")
	if len(sieve) == 0 {
		sieve = defaultSieve()
	}
	rules := stateSlice(sieve, "rules")
	vacation := stateMap(sieve, "vacation")
	if len(vacation) == 0 {
		vacation = map[string]any{"enabled": false, "start": "", "end": "", "days": 7,
			"internal": map[string]any{"subject": "Out of office", "message": ""},
			"external": map[string]any{"enabled": false, "subject": "Away from the office", "message": ""}}
	}
	forwarding := stateMap(sieve, "forwarding")
	if len(forwarding) == 0 {
		forwarding = defaultForwarding()
	}
	script := stateString(sieve, "script")
	if script == "" {
		script = renderSieve(sieve, email)
	}
	return map[string]any{
		"revision": stateInt(sieve, "revision"), "updatedAt": sieve["updatedAt"],
		"rules": rules, "vacation": vacation,
		"forwarding": publicForwarding(forwarding), "script": script,
	}
}
