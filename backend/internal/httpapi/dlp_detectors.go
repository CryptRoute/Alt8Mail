package httpapi

import (
	"regexp"
	"strings"
)

var (
	dlpCreditCardPattern = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	dlpIBANPattern       = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)
	dlpSSNPattern        = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	dlpNationalIDPattern = regexp.MustCompile(`(?i)\b[A-CEGHJ-PR-TW-Z]{2}\d{6}[A-D]\b`)
)

type dlpDetectorHit struct {
	Display  string
	Detector string
}

func dlpMaskedValue(value string) string {
	digits := []rune{}
	for _, char := range value {
		if (char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') {
			digits = append(digits, char)
		}
	}
	if len(digits) <= 4 {
		return "••••"
	}
	return "•••• " + string(digits[len(digits)-4:])
}

func luhnValid(value string) bool {
	digits := []int{}
	for _, char := range value {
		if char >= '0' && char <= '9' {
			digits = append(digits, int(char-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for index := len(digits) - 1; index >= 0; index-- {
		digit := digits[index]
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

func ibanValid(value string) bool {
	compact := strings.ToUpper(strings.Join(strings.Fields(value), ""))
	if len(compact) < 15 || len(compact) > 34 {
		return false
	}
	for _, char := range compact {
		if !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') {
			return false
		}
	}
	rearranged := compact[4:] + compact[:4]
	remainder := 0
	for _, char := range rearranged {
		value := int(char)
		if char >= 'A' && char <= 'Z' {
			value = int(char-'A') + 10
		} else {
			value = int(char - '0')
		}
		if value >= 10 {
			remainder = (remainder*100 + value) % 97
		} else {
			remainder = (remainder*10 + value) % 97
		}
	}
	return remainder == 1
}

func dlpDetectorMatches(detector string, rule adminDLPRule, haystack string) []dlpDetectorHit {
	hits := []dlpDetectorHit{}
	seen := map[string]bool{}
	add := func(display string) {
		if display == "" || seen[display] {
			return
		}
		seen[display] = true
		hits = append(hits, dlpDetectorHit{Display: display, Detector: detector})
	}
	switch detector {
	case "", "terms":
		for _, term := range rule.Terms {
			if term != "" && strings.Contains(haystack, strings.ToLower(term)) {
				add(term)
			}
		}
	case "dictionary":
		for _, term := range rule.Terms {
			if term == "" {
				continue
			}
			pattern, err := regexp.Compile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)
			if err != nil {
				if strings.Contains(haystack, strings.ToLower(term)) {
					add(term)
				}
				continue
			}
			if pattern.MatchString(haystack) {
				add(term)
			}
		}
	case "regex":
		if rule.Pattern == "" {
			return hits
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return hits
		}
		for _, match := range pattern.FindAllString(haystack, 5) {
			add("[redacted]")
			_ = match
		}
	case "credit_card":
		for _, match := range dlpCreditCardPattern.FindAllString(haystack, 10) {
			if luhnValid(match) {
				add(dlpMaskedValue(match))
			}
		}
	case "iban":
		for _, match := range dlpIBANPattern.FindAllString(strings.ToUpper(haystack), 10) {
			if ibanValid(match) {
				add(dlpMaskedValue(match))
			}
		}
	case "national_id":
		for _, match := range dlpSSNPattern.FindAllString(haystack, 10) {
			add(dlpMaskedValue(match))
		}
		for _, match := range dlpNationalIDPattern.FindAllString(haystack, 10) {
			add(dlpMaskedValue(match))
		}
	}
	return hits
}

func redactDLPDetectors(value string, policy adminTenantPolicy) string {
	replace := func(pattern *regexp.Regexp, validate func(string) bool) {
		value = pattern.ReplaceAllStringFunc(value, func(match string) string {
			if validate == nil || validate(match) {
				return "[REDACTED]"
			}
			return match
		})
	}
	for _, rule := range policy.DLP.Rules {
		switch rule.Detector {
		case "credit_card":
			replace(dlpCreditCardPattern, luhnValid)
		case "iban":
			replace(dlpIBANPattern, ibanValid)
		case "national_id":
			replace(dlpSSNPattern, nil)
			replace(dlpNationalIDPattern, nil)
		case "regex":
			if pattern, err := regexp.Compile(rule.Pattern); err == nil {
				replace(pattern, nil)
			}
		}
	}
	return value
}
