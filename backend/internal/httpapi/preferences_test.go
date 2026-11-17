package httpapi

import "testing"

func TestPreferencesDefaultInboxViewIsGrouped(t *testing.T) {
	prefs := preferencesFor(userRecord{})
	if prefs["inboxView"] != "grouped" {
		t.Fatalf("default inboxView = %v, want grouped", prefs["inboxView"])
	}
	if prefs["composeMode"] != "html" {
		t.Fatalf("default composeMode = %v, want html", prefs["composeMode"])
	}
	if prefs["cabinetView"] != "list" {
		t.Fatalf("default cabinetView = %v, want list", prefs["cabinetView"])
	}
}
