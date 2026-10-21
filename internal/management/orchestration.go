package management

import (
	"context"
	"slices"

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
		commit, err := s.accounts.Import(ctx, c.ImportCommand{Operation: op, Credentials: in.Credentials[index], Proxy: in.proxy(index)})
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
		out, err := s.verify(ctx, c.VerificationInput{Account: account, Candidate: &material, Mode: c.VerificationModel, Model: in.Model, Channels: in.Channels}, op.Progress, taskID, index)
		return verificationItem(out), err
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
			out, err := s.verify(ctx, c.VerificationInput{Account: account, Candidate: &material, Mode: c.VerificationModel, Model: in.Model, Channels: in.Channels}, op.Progress, taskID, index)
			if err != nil {
				return verificationItem(out), err
			}
			candidate = &material
			if out.RefreshedCandidate != nil {
				candidate = out.RefreshedCandidate
			}
			verified = &out.Result
		}
		commit, err := s.accounts.Replace(ctx, c.ReplaceCommand{Operation: op, AccountID: item.AccountID, Credentials: in.Credentials[0], Proxy: in.proxy(0), Expected: expected, Enable: !in.Legacy && (in.proxy(0) == nil || in.Verify), Candidate: candidate})
		if err != nil {
			if verified != nil {
				verified.FailFinalization(c.PublicError(err).Code)
				return &c.ItemResult{Verification: verified}, err
			}
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
			// Replace committed this exact private candidate under the captured
			// dual CAS. Only its original durable receipt may project the evidence
			// onto the committed lineage; a live account read cannot do so.
			if candidate == nil || commit.Account.AccountID != account.AccountID || commit.Account.NativeID != account.NativeID || commit.Account.Index != receipt.Index || commit.Account.Versions != receipt.Versions {
				verified.FailFinalization(c.CodePersistenceError)
				return &c.ItemResult{Verification: verified}, c.NewError(c.CodePersistenceError)
			}
			verified.Versions = receipt.Versions
			verified.AuthIndex = receipt.Index
			for i := range verified.ChannelResults {
				verified.ChannelResults[i].Versions = receipt.Versions
				verified.ChannelResults[i].AuthIndex = receipt.Index
			}
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
		out, err := s.verify(ctx, c.VerificationInput{Account: account, Candidate: &material, Mode: mode, Model: in.Model, Channels: in.Channels}, op.Progress, taskID, index)
		return verificationItem(out), err
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
func verificationItem(out c.VerificationOutput) *c.ItemResult {
	if out.Result.Model == "" {
		return nil
	}
	return &c.ItemResult{Verification: &out.Result}
}

func (s *taskService) verify(ctx context.Context, input c.VerificationInput, progress c.ProgressFunc, taskID string, index int) (c.VerificationOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, c.VerificationTimeout)
	defer cancel()
	channels, err := c.NormalizeVerificationChannels(input.Channels)
	if err != nil {
		return c.VerificationOutput{}, err
	}
	input.Channels = channels
	input.Checkpoint = func(result c.VerificationResult) error {
		if !validVerificationForInput(input, result, false) {
			return c.NewError(c.CodeVerificationFailed)
		}
		_, err := s.changeChecked(taskID, func(r *c.TaskRecord) error {
			if index < 0 || index >= len(r.Items) {
				return c.NewError(c.CodeInvalidState)
			}
			item := &r.Items[index]
			// Attribution comes from the captured account, never a child result.
			trustedIndex := input.Account.Index
			if item.AccountID != input.Account.AccountID || item.Index != nil && *item.Index != trustedIndex {
				return c.NewError(c.CodeInvalidState)
			}
			item.Index = &trustedIndex
			item.Result = &c.ItemResult{Verification: &result}
			return nil
		})
		if err != nil {
			return c.NewError(c.CodePersistenceError)
		}
		clean, stop := cleanupContext()
		defer stop()
		latest, err := s.store.GetAccount(clean, input.Account.AccountID)
		if err != nil {
			return err
		}
		if latest.Versions != input.Account.Versions || latest.Deleted || latest.Archived {
			return c.NewError(c.CodeVersionConflict)
		}
		return nil
	}
	out, err := s.verifier.Verify(ctx, input, progress)
	if out.Result.Model != "" && !validVerificationForInput(input, out.Result, err == nil) {
		return c.VerificationOutput{}, c.NewError(c.CodeVerificationFailed)
	}
	if err != nil {
		return out, err
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if !validVerificationForInput(input, out.Result, true) {
		return c.VerificationOutput{}, c.NewError(c.CodeVerificationFailed)
	}
	if checkpointErr := input.Checkpoint(out.Result); checkpointErr != nil {
		if len(out.Result.Channels) > 0 {
			out.Result.Success = false
			out.Result.Stage = "failed"
		}
		return out, checkpointErr
	}
	return out, nil
}

func validVerificationForInput(input c.VerificationInput, r c.VerificationResult, requireSuccess bool) bool {
	if !c.ValidVerificationResult(r) || r.AuthIndex != input.Account.Index || r.Model != input.Model || r.Versions != input.Account.Versions || (requireSuccess && !r.Success) {
		return false
	}
	stage := "model_verified"
	if input.Mode == c.VerificationConnection {
		stage = "connection_verified"
	}
	if r.Success && r.Stage != stage {
		return false
	}
	if len(r.Channels) == 0 {
		return len(input.Channels) == 1 && r.ActualChannel == input.Channels[0]
	}
	if !slices.Equal(input.Channels, r.Channels) {
		return false
	}
	for _, child := range r.ChannelResults {
		if child.Success && child.Stage != stage {
			return false
		}
	}
	return true
}
