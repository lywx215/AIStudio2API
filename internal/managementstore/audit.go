package managementstore

import (
	"context"
	"encoding/json"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (s *store) AppendAudit(ctx context.Context, e c.AuditEntry) error {
	if !c.ValidID(e.AuditID) || !c.ValidRequestID(e.RequestID) || !c.ValidID(e.CreatedByKeyID) || e.Timestamp.IsZero() || e.AccountID != "" && !c.ValidAccountID(e.AccountID) || e.TaskID != "" && !c.ValidID(e.TaskID) {
		return c.NewError(c.CodeInvalidRequest)
	}
	switch e.Action {
	case c.AuditLoginPasswordWrite, c.AuditLoginPasswordClear, c.AuditLoginTotpWrite, c.AuditLoginTotpClear:
	case "import", "replace", "test", "archive", "restore", "reload", "reload-auth", "batch", "patch", "export", "account-patch", "settings-patch", "accounts.patch", "accounts.export", "account.patch", "account.export", "cancel", "task.cancel", "key.create", "key.revoke":
	default:
		return c.NewError(c.CodeInvalidRequest)
	}
	switch e.Outcome {
	case "succeeded", "failed", "cancelled", "interrupted":
	default:
		return c.NewError(c.CodeInvalidRequest)
	}
	if e.Error != nil {
		e.Error = c.PublicError(e.Error)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return c.NewError(c.CodeInvalidRequest)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO audit(audit_id,timestamp,entry) VALUES(?,?,?)", e.AuditID, e.Timestamp.UnixMilli(), b); err != nil {
		return c.NewError(c.CodeAuditPersistenceFailed)
	}
	return nil
}
func (s *store) ListAudit(ctx context.Context, q c.PageQuery) (c.Page[c.AuditEntry], error) {
	return page(ctx, s.db, q, "SELECT COUNT(*) FROM audit", "SELECT entry FROM audit ORDER BY timestamp DESC,audit_id DESC LIMIT ? OFFSET ?", decodeJSON[c.AuditEntry])
}
