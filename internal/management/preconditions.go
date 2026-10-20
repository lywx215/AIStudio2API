package management

import (
	"context"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func taskPreconditions(work c.TaskWork) (map[int]*c.TaskPrecondition, error) {
	result := map[int]*c.TaskPrecondition{}
	if len(work.Record.Items) == 0 || len(work.Record.Items) > c.MaxBatchItems {
		return nil, c.NewError(c.CodeInterrupted)
	}
	for _, item := range work.Record.Items {
		if item.Status != c.TaskQueued {
			return nil, c.NewError(c.CodeInterrupted)
		}
	}
	set := work.PreconditionSet
	if set == nil {
		if work.Record.Kind == c.TaskImport && len(work.Preconditions) == 0 {
			return result, nil
		}
		return nil, c.NewError(c.CodeInterrupted)
	}
	if set.FormatVersion != c.TaskPreconditionFormatVersion || set.Mode != captureMode(work.Record.Kind) || set.TargetCount < 0 || uint64(set.TargetCount) > c.MaxSafeInteger {
		return nil, c.NewError(c.CodeInterrupted)
	}
	switch work.Record.Kind {
	case c.TaskImport:
		if set.TargetCount != 0 || len(work.Preconditions) != 0 {
			return nil, c.NewError(c.CodeInterrupted)
		}
	case c.TaskReloadAuth:
		if len(work.Record.Items) != 1 || len(work.Preconditions) != 0 {
			return nil, c.NewError(c.CodeInterrupted)
		}
	default:
		if set.TargetCount != len(work.Record.Items) || len(work.Preconditions) != set.TargetCount {
			return nil, c.NewError(c.CodeInterrupted)
		}
		for _, precondition := range work.Preconditions {
			p := precondition
			if p.ItemIndex < 0 || p.ItemIndex >= len(work.Record.Items) || result[p.ItemIndex] != nil || p.AccountID != work.Record.Items[p.ItemIndex].AccountID || !c.ValidAccountID(p.AccountID) || p.Ordinal != int64(p.ItemIndex) || !validPrecondition(p, work.Record.Kind) {
				return nil, c.NewError(c.CodeInterrupted)
			}
			result[p.ItemIndex] = &p
		}
	}
	return result, nil
}

func captureMode(kind c.TaskKind) c.TaskCaptureMode {
	switch kind {
	case c.TaskImport:
		return c.TaskCaptureNone
	case c.TaskReloadAuth:
		return c.TaskCaptureReloadAuth
	default:
		return c.TaskCaptureItems
	}
}

func validPrecondition(p c.TaskPrecondition, kind c.TaskKind) bool {
	if p.ClientExpected.Validate(false) != nil {
		return false
	}
	if p.Missing {
		return p.SnapshotKnown && p.CredentialVersion == 0 && p.StateVersion == 0
	}
	if !p.SnapshotKnown {
		return kind == c.TaskReplace && p.CredentialVersion == 0 && p.StateVersion == 0 && p.ClientExpected.Validate(true) == nil
	}
	return p.CredentialVersion > 0 && p.StateVersion > 0 && p.CredentialVersion <= c.MaxSafeInteger && p.StateVersion <= c.MaxSafeInteger
}

func (s *taskService) checkPrecondition(ctx context.Context, p *c.TaskPrecondition, id string) error {
	if p == nil || p.AccountID != id {
		return c.NewError(c.CodeInterrupted)
	}
	if p.Missing {
		return c.NewError(c.CodeNotFound)
	}
	current, err := s.store.GetAccount(ctx, id)
	if err != nil {
		if c.PublicError(err).Code == c.CodeNotFound {
			return c.NewError(c.CodeVersionConflict)
		}
		return err
	}
	return p.Check(current)
}

func (s *taskService) reloadFixedTargets(ctx context.Context, taskID string, count int, op c.OperationContext) error {
	var firstError error
	var previousOrdinal int64 = -1
	for offset := 0; offset < count; {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		page, err := s.store.ListTaskPreconditions(ctx, taskID, c.PageQuery{Offset: offset, Limit: c.MaxPageLimit})
		if err != nil {
			return err
		}
		if page.Total != count || page.Offset != offset || len(page.Items) == 0 || len(page.Items) > c.MaxPageLimit || offset+len(page.Items) > count {
			return c.NewError(c.CodeInterrupted)
		}
		for _, p := range page.Items {
			if p.ItemIndex != 0 || p.Ordinal != previousOrdinal+1 || !c.ValidAccountID(p.AccountID) || !validPrecondition(p, c.TaskReloadAuth) {
				return c.NewError(c.CodeInterrupted)
			}
			previousOrdinal = p.Ordinal
			if err := s.checkPrecondition(ctx, &p, p.AccountID); err != nil {
				if firstError == nil {
					firstError = err
				}
				continue
			}
			result, err := s.accounts.Reload(ctx, c.StateCommand{Operation: op, AccountID: p.AccountID, Expected: p.Expected()})
			if err == nil && !result.Published {
				err = c.NewError(c.CodePersistenceError)
			}
			if err != nil && firstError == nil {
				firstError = err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		offset += len(page.Items)
	}
	return firstError
}
