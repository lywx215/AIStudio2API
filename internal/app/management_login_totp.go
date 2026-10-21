package app

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/logintotp"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

var _ contract.LoginTotpCoordinator = (*managementAccountCoordinator)(nil)

func (c *managementAccountCoordinator) loginTotpCredentials(ctx context.Context, id string) (aistudio.LoginTotpCredentials, string, error) {
	record, err := c.store.GetAccount(ctx, id)
	if err != nil {
		return aistudio.LoginTotpCredentials{}, "", err
	}
	if record.Deleted {
		return aistudio.LoginTotpCredentials{}, "", contract.NewError(contract.CodeNotFound)
	}
	directory, err := aistudio.AccountDirectory(c.paths, record.NativeID, false)
	if err != nil {
		return aistudio.LoginTotpCredentials{}, "", contract.NewError(contract.CodePersistenceError)
	}
	stored, err := aistudio.ReadLoginTotpCredentials(directory, record.NativeID)
	if err != nil || stored.Version != 0 && stored.AccountID != id {
		return aistudio.LoginTotpCredentials{}, "", contract.NewError(contract.CodePersistenceError)
	}
	stored.AccountID = id
	return stored, directory, nil
}

func (c *managementAccountCoordinator) LoginTotp(ctx context.Context, id string) (contract.LoginTotpStatus, error) {
	c.bind.RLock()
	defer c.bind.RUnlock()
	c.gate.Lock()
	defer c.gate.Unlock()
	stored, _, err := c.loginTotpCredentials(ctx, id)
	return stored.Status(), err
}

func (c *managementAccountCoordinator) WriteLoginTotp(ctx context.Context, id string, input contract.LoginTotpInput, clear bool) (contract.LoginTotpStatus, error) {
	if input.ExpectedVersion == nil || *input.ExpectedVersion > contract.MaxSafeInteger || !contract.ValidAccountID(input.OperationID) || !utf8.ValidString(input.Email) || !clear && (input.Secret == nil || *input.Secret == "" || len(*input.Secret) > 16384 || !utf8.ValidString(*input.Secret) || !logintotp.Valid(*input.Secret)) || clear && input.Secret != nil {
		return contract.LoginTotpStatus{}, contract.NewError(contract.CodeInvalidRequest)
	}
	c.bind.RLock()
	defer c.bind.RUnlock()
	c.gate.Lock()
	defer c.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return contract.LoginTotpStatus{}, err
	}
	stored, directory, err := c.loginTotpCredentials(ctx, id)
	if err != nil {
		return contract.LoginTotpStatus{}, err
	}
	release, err := aistudio.AcquireLoginCredentialsLease(directory)
	if err != nil {
		if errors.Is(err, aistudio.ErrAccountLeased) {
			return contract.LoginTotpStatus{}, contract.NewError(contract.CodeAccountBusy)
		}
		return contract.LoginTotpStatus{}, contract.NewError(contract.CodePersistenceError)
	}
	defer release()
	// Another process may have committed between the first read and the lease.
	stored, directory, err = c.loginTotpCredentials(ctx, id)
	if err != nil {
		return contract.LoginTotpStatus{}, err
	}
	if strings.ToLower(strings.TrimSpace(input.Email)) != stored.Email {
		return contract.LoginTotpStatus{}, contract.NewError(contract.CodeInvalidCredentials)
	}
	secret := ""
	if !clear {
		secret = *input.Secret
	}
	if stored.LastOperationID == input.OperationID {
		if stored.Secret != secret || stored.Version != *input.ExpectedVersion+1 {
			return contract.LoginTotpStatus{}, contract.NewError(contract.CodeIdempotencyConflict)
		}
		return stored.Status(), nil
	}
	if stored.Version != *input.ExpectedVersion || stored.Version == contract.MaxSafeInteger {
		return contract.LoginTotpStatus{}, contract.NewError(contract.CodeVersionConflict)
	}
	stored.Secret, stored.LastOperationID, stored.Version = secret, input.OperationID, stored.Version+1
	if err := aistudio.WriteLoginTotpCredentials(directory, stored); err != nil {
		return contract.LoginTotpStatus{}, contract.NewError(contract.CodePersistenceError)
	}
	return stored.Status(), nil
}
