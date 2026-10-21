package managementstore

import (
	"context"
	"testing"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestLoginPasswordOperationsPersistAudit(t *testing.T) {
	store := openTest(t, "")
	ctx := context.Background()
	key := createTestKey(t, store)
	for _, action := range []string{c.AuditLoginPasswordWrite, c.AuditLoginPasswordClear} {
		entry := c.AuditEntry{AuditID: id(t, "audit"), RequestID: id(t, "req"), CreatedByKeyID: key.Key.ID, Action: action, Timestamp: store.now(), Outcome: "succeeded"}
		if err := store.AppendAudit(ctx, entry); err != nil {
			t.Fatal("password operation audit rejected", err)
		}
	}
	page, err := store.ListAudit(ctx, c.PageQuery{Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatal("password operation audit missing", err)
	}
}
