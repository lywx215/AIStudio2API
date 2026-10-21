package managementstore

import (
	"context"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// ReadProxyCommit is an internal read-only seam for console operation recovery.
// It exposes neither the file journal nor any proxy/credential material. A
// planned/rolled-back operation is never promoted using the current account.
func (s *store) ReadProxyCommit(ctx context.Context, operationID string) (c.AccountCommitResult, bool, error) {
	if !c.ValidID(operationID) {
		return c.AccountCommitResult{}, false, c.NewError(c.CodeInvalidRequest)
	}
	r, _, err := readJournal(ctx, s.db, operationID)
	if isCode(err, c.CodeNotFound) {
		return c.AccountCommitResult{}, false, nil
	}
	if err != nil {
		return c.AccountCommitResult{}, false, err
	}
	if r.Phase != c.CommitCommitted || !r.Plan.Intent.ProxyApplied {
		return c.AccountCommitResult{}, false, nil
	}
	result, err := s.committedResult(ctx, r)
	return result, err == nil, err
}
