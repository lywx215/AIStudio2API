package app

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

var _ contract.LoginPasswordCoordinator = (*managementAccountCoordinator)(nil)

func (c *managementAccountCoordinator) loginCredentials(ctx context.Context, id string) (aistudio.LoginCredentials, string, error) {
	record, err := c.store.GetAccount(ctx, id)
	if err != nil {
		return aistudio.LoginCredentials{}, "", err
	}
	if record.Deleted {
		return aistudio.LoginCredentials{}, "", contract.NewError(contract.CodeNotFound)
	}
	directory, err := aistudio.AccountDirectory(c.paths, record.NativeID, false)
	if err != nil {
		return aistudio.LoginCredentials{}, "", contract.NewError(contract.CodePersistenceError)
	}
	stored, err := aistudio.ReadLoginCredentials(directory, record.NativeID)
	if err != nil || stored.Version != 0 && stored.AccountID != id {
		return aistudio.LoginCredentials{}, "", contract.NewError(contract.CodePersistenceError)
	}
	stored.AccountID = id
	return stored, directory, nil
}

func (c *managementAccountCoordinator) LoginPassword(ctx context.Context, id string) (contract.LoginPasswordStatus, error) {
	c.bind.RLock()
	defer c.bind.RUnlock()
	c.gate.Lock()
	defer c.gate.Unlock()
	stored, _, err := c.loginCredentials(ctx, id)
	return stored.Status(), err
}

func (c *managementAccountCoordinator) WriteLoginPassword(ctx context.Context, id string, input contract.LoginPasswordInput, clear bool) (contract.LoginPasswordStatus, error) {
	if input.ExpectedVersion == nil || *input.ExpectedVersion > contract.MaxSafeInteger || !contract.ValidAccountID(input.OperationID) || !utf8.ValidString(input.Email) || !clear && (input.Password == nil || *input.Password == "" || len(*input.Password) > 16384 || !utf8.ValidString(*input.Password)) || clear && input.Password != nil {
		return contract.LoginPasswordStatus{}, contract.NewError(contract.CodeInvalidRequest)
	}
	c.bind.RLock()
	defer c.bind.RUnlock()
	c.gate.Lock()
	defer c.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.LoginPasswordStatus{}, err
	}
	stored, directory, err := c.loginCredentials(ctx, id)
	if err != nil {
		return contract.LoginPasswordStatus{}, err
	}
	release, err := aistudio.AcquireLoginCredentialsLease(directory)
	if err != nil {
		if errors.Is(err, aistudio.ErrAccountLeased) {
			return contract.LoginPasswordStatus{}, contract.NewError(contract.CodeAccountBusy)
		}
		return contract.LoginPasswordStatus{}, contract.NewError(contract.CodePersistenceError)
	}
	defer release()
	// Another process may have committed between the first read and the lease.
	stored, directory, err = c.loginCredentials(ctx, id)
	if err != nil {
		return contract.LoginPasswordStatus{}, err
	}
	if strings.ToLower(strings.TrimSpace(input.Email)) != stored.Email {
		return contract.LoginPasswordStatus{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	password := ""
	if !clear {
		password = *input.Password
	}
	if stored.LastOperationID == input.OperationID {
		if stored.Password != password || stored.Version != *input.ExpectedVersion+1 {
			return contract.LoginPasswordStatus{}, contract.NewError(contract.CodeIdempotencyConflict)
		}
		return stored.Status(), nil
	}
	if stored.Version != *input.ExpectedVersion || stored.Version == contract.MaxSafeInteger {
		return contract.LoginPasswordStatus{}, contract.NewError(contract.CodeVersionConflict)
	}
	stored.Password, stored.LastOperationID, stored.Version = password, input.OperationID, stored.Version+1
	if err := aistudio.WriteLoginCredentials(directory, stored); err != nil {
		return contract.LoginPasswordStatus{}, contract.NewError(contract.CodePersistenceError)
	}
	return stored.Status(), nil
}
