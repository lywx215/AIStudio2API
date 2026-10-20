package management

import (
	"context"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

// committed persists the original receipt before any independent verification.
// Cleanup uses its own bound because the account commit may win a cancellation.
func (s *taskService) committed(op c.OperationContext) (*c.UploadReceipt, error) {
	ctx, cancel := cleanupContext()
	defer cancel()
	receipt, err := s.store.GetUploadReceipt(ctx, *op.Upload)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, c.NewError(c.CodePersistenceError)
	}
	_, err = s.change(op.Upload.TaskID, func(r *c.TaskRecord) { attachReceipt(&r.Items[op.Upload.ItemIndex], receipt) })
	if err != nil {
		return receipt, err
	}
	// Readback confirms the stable mapping. Live versions may already be newer;
	// they must never replace this upload's immutable versions or timestamp.
	record, err := s.store.GetAccount(ctx, receipt.AccountID)
	if err != nil || record.AccountID != receipt.AccountID || record.Index != receipt.Index {
		return receipt, c.NewError(c.CodePersistenceError)
	}
	return receipt, nil
}

func (s *taskService) executeItem(ctx context.Context, kind c.TaskKind, item c.TaskItem, index int, in taskInput, op c.OperationContext, taskID string, precondition *c.TaskPrecondition, preconditionCount int) (*c.ItemResult, error) {
	if kind != c.TaskImport && kind != c.TaskReloadAuth {
		if err := s.checkPrecondition(ctx, precondition, item.AccountID); err != nil {
			return nil, err
		}
	}
	switch kind {
	case c.TaskImport:
		if index >= len(in.Credentials) {
			return nil, invalid()
		}
		commit, err := s.accounts.Import(ctx, c.ImportCommand{Operation: op, Credentials: in.Credentials[index]})
		// A coordinator may return an error after durable commit/publication.
		if err != nil {
			return nil, err
		}
		receipt, err := s.committed(op)
		if err != nil {
			return nil, err
		}
		if !commit.Published {
			return nil, c.NewError(c.CodePersistenceError)
		}
		if !in.Verify {
			return uploadResult(receipt), nil
		}
		account, material, err := s.accounts.SnapshotCredentials(ctx, receipt.AccountID)
		if err != nil {
			return nil, err
		}
		if account.Versions != receipt.Versions {
			return nil, c.NewError(c.CodeVersionConflict)
		}
		out, err := s.verify(ctx, c.VerificationInput{Account: account, Candidate: &material, Mode: c.VerificationModel, Model: in.Model}, op.Progress)
		if err != nil {
			return nil, err
		}
		return &c.ItemResult{Verification: &out.Result}, nil
	case c.TaskReplace:
		if len(in.Credentials) != 1 {
			return nil, invalid()
		}
		account, _, err := s.accounts.SnapshotCredentials(ctx, item.AccountID)
		if err != nil {
			return nil, err
		}
		if account.Archived {
			return nil, c.NewError(c.CodeInvalidState)
		}
		if err := precondition.Check(account); err != nil {
			return nil, err
		}
		expected := in.Expected
		if err = expected.Validate(!in.Legacy); err != nil {
			return nil, err
		}
		if expected.Credential != nil && (*expected.Credential != account.CredentialVersion || *expected.State != account.StateVersion) {
			return nil, c.NewError(c.CodeVersionConflict)
		}
		// Legacy callers also acquire dual CAS before network work. They preserve
		// flags, but cannot overwrite a later replacement or manual disable.
		expected = precondition.Expected()
		var candidate *c.CredentialMaterial
		var verified *c.VerificationResult
		if in.Verify {
			material, err := s.accounts.PrepareCredentials(in.Credentials[0])
			if err != nil {
				return nil, err
			}
			if material.NativeID != account.NativeID {
				return nil, c.NewError(c.CodeInvalidCredentials)
			}
			out, err := s.verify(ctx, c.VerificationInput{Account: account, Candidate: &material, Mode: c.VerificationModel, Model: in.Model}, op.Progress)
			if err != nil {
				return nil, err
			}
			candidate = &material
			if out.RefreshedCandidate != nil {
				candidate = out.RefreshedCandidate
			}
			verified = &out.Result
		}
		commit, err := s.accounts.Replace(ctx, c.ReplaceCommand{Operation: op, AccountID: item.AccountID, Credentials: in.Credentials[0], Expected: expected, Enable: !in.Legacy, Candidate: candidate})
		if err != nil {
			return nil, err
		}
		receipt, err := s.committed(op)
		if err != nil {
			return nil, err
		}
		if !commit.Published {
			return nil, c.NewError(c.CodePersistenceError)
		}
		if verified != nil {
			verified.Versions = receipt.Versions
			verified.AuthIndex = receipt.Index
			return &c.ItemResult{Verification: verified}, nil
		}
		return uploadResult(receipt), nil
	case c.TaskTest:
		account, material, err := s.accounts.SnapshotCredentials(ctx, item.AccountID)
		if err != nil {
			return nil, err
		}
		if account.Archived {
			return nil, c.NewError(c.CodeInvalidState)
		}
		if err := precondition.Check(account); err != nil {
			return nil, err
		}
		mode := c.VerificationMode(in.Action)
		out, err := s.verify(ctx, c.VerificationInput{Account: account, Candidate: &material, Mode: mode, Model: in.Model}, op.Progress)
		if err != nil {
			return nil, err
		}
		return &c.ItemResult{Verification: &out.Result}, nil
	case c.TaskArchive, c.TaskRestore, c.TaskReload, c.TaskBatch:
		command := c.StateCommand{Operation: op, AccountID: item.AccountID, Force: in.Force, Expected: precondition.Expected()}
		var result c.AccountCommitResult
		var err error
		switch {
		case kind == c.TaskArchive || kind == c.TaskBatch && in.Action == "archive":
			result, err = s.accounts.Archive(ctx, command)
		case kind == c.TaskRestore:
			result, err = s.accounts.Restore(ctx, command)
		case kind == c.TaskReload:
			result, err = s.accounts.Reload(ctx, command)
		default:
			command.Enabled = in.Action == "enable"
			result, err = s.accounts.Patch(ctx, command)
		}
		if err == nil && !result.Published {
			err = c.NewError(c.CodePersistenceError)
		}
		return nil, err
	case c.TaskReloadAuth:
		return nil, s.reloadFixedTargets(ctx, taskID, preconditionCount, op)
	}
	return nil, invalid()
}

func uploadResult(receipt *c.UploadReceipt) *c.ItemResult {
	return &c.ItemResult{Upload: &c.UploadOnlyResult{Success: true, Stage: "uploaded", Versions: receipt.Versions}}
}

// The verifier owns upstream observation checks; this boundary additionally
// refuses a collaborator returning a successful result for another target.
func (s *taskService) verify(ctx context.Context, input c.VerificationInput, progress c.ProgressFunc) (c.VerificationOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, c.VerificationTimeout)
	defer cancel()
	out, err := s.verifier.Verify(ctx, input, progress)
	if err != nil {
		return out, err
	}
	r := out.Result
	stage := "model_verified"
	if input.Mode == c.VerificationConnection {
		stage = "connection_verified"
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if !r.Success || r.AuthIndex != input.Account.Index || r.Model != input.Model || r.ActualChannel != c.ChannelPlayground || r.Stage != stage || !c.ValidRequestID(r.RequestID) || r.Versions != input.Account.Versions || r.UpstreamStatus == nil || *r.UpstreamStatus < 200 || *r.UpstreamStatus >= 300 {
		return out, c.NewError(c.CodeVerificationFailed)
	}
	return out, nil
}
