package httpapi

import (
	"fmt"
	"testing"
)

func BenchmarkUnifiedFolderCondition(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		where, args := mailFolderConditionForAccount("00000000-0000-0000-0000-000000000001", mailAccountScopeAll, "inbox", "unread")
		if where == "" || len(args) == 0 {
			b.Fatal("empty condition")
		}
	}
}

func BenchmarkDedupeThreadMessages(b *testing.B) {
	messages := make([]map[string]any, 0, 2000)
	for i := 0; i < 2000; i++ {
		account := "macct_1"
		if (i/2)%3 == 0 {
			account = "macct_2"
		}
		messages = append(messages, map[string]any{
			"id": fmt.Sprintf("msg-%d", i), "accountId": account,
			"messageId": fmt.Sprintf("<bulk-%d@example.test>", i/2),
			"folder":    []string{"inbox", "mfld_label"}[i%2],
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := dedupeThreadMessages(messages, "inbox"); len(got) != 1000 {
			b.Fatalf("deduped = %d, want 1000", len(got))
		}
	}
}
