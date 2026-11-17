package httpapi

import (
	"testing"
	"time"
)

func TestRetentionByClassification(t *testing.T) {
	now := time.Now().UTC()
	policy := adminMailPolicy{
		RetentionDays:             365,
		RetentionByClassification: map[string]int{"Confidential": 30, "Restricted": 90},
	}
	if !retentionDue("Confidential", now.AddDate(0, 0, -31), policy, now) {
		t.Fatal("confidential message past its class window was not due")
	}
	if retentionDue("Confidential", now.AddDate(0, 0, -20), policy, now) {
		t.Fatal("confidential message inside its class window was due")
	}
	if retentionDue("Internal", now.AddDate(0, 0, -100), policy, now) {
		t.Fatal("internal message was due before the global window")
	}
	if !retentionDue("Internal", now.AddDate(0, 0, -366), policy, now) {
		t.Fatal("internal message past the global window was not due")
	}
	if min := retentionMinDays(policy); min != 30 {
		t.Fatalf("min days = %d", min)
	}
	classOnly := adminMailPolicy{RetentionDays: 0, RetentionByClassification: map[string]int{"Restricted": 90}}
	if min := retentionMinDays(classOnly); min != 90 {
		t.Fatalf("class-only min days = %d", min)
	}
	if retentionDue("Public", now.AddDate(0, 0, -400), classOnly, now) {
		t.Fatal("class-only policy retained unclassified mail by the global window")
	}
}
