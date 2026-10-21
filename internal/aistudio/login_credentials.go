package aistudio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (s *AccountStore) LoadLoginCredentials(ctx context.Context, account *Account) (LoginCredentials, error) {
	stored, err := ReadLoginCredentials(account.Directory, account.ID)
	if err != nil || stored.Version == 0 {
		return stored, err
	}
	if s.writer != nil {
		record, err := s.writer.SnapshotNative(ctx, account.ID)
		if err != nil || record.Deleted || record.AccountID != stored.AccountID {
			return LoginCredentials{}, errors.New("login credentials identity mismatch")
		}
	}
	return stored, nil
}

const LoginCredentialsName = "login-credentials.json"

func isLoginCredentialsFile(name string) bool {
	return name == LoginTotpCredentialsName || strings.HasPrefix(name, "."+LoginTotpCredentialsName+"-") && strings.HasSuffix(name, ".tmp") || name == LoginCredentialsName || strings.HasPrefix(name, "."+LoginCredentialsName+"-") && strings.HasSuffix(name, ".tmp")
}

// AcquireLoginCredentialsLease shares the existing cross-process account lease
// with Cookie writes and deletion. It never waits while a coordinator gate is held.
func AcquireLoginCredentialsLease(directory string) (func() error, error) {
	lock, _, err := acquireAccountFileLease(filepath.Join(directory, storageStateName))
	if err != nil {
		return nil, err
	}
	return func() error {
		if lock == nil {
			return nil
		}
		return lock.Unlock()
	}, nil
}

// LoginCredentials is deliberately separate from exported Cookie credentials.
// Password is stored verbatim on disk, as requested by the account owner.
type LoginCredentials struct {
	Email           string `json:"email"`
	Password        string `json:"password"`
	AccountID       string `json:"accountId"`
	Version         uint64 `json:"version"`
	LastOperationID string `json:"lastOperationId"`
}

func (LoginCredentials) String() string   { return "[login credentials]" }
func (LoginCredentials) GoString() string { return "[login credentials]" }

func (c LoginCredentials) Status() contract.LoginPasswordStatus {
	return contract.LoginPasswordStatus{Configured: c.Password != "", Version: c.Version, LastOperationID: c.LastOperationID}
}

func ReadLoginCredentials(directory, email string) (LoginCredentials, error) {
	if err := validateAccountPathAncestors(directory); err != nil {
		return LoginCredentials{}, errors.New("invalid login credentials directory")
	}
	if info, err := os.Lstat(filepath.Join(directory, LoginCredentialsName)); err == nil && info.Size() > 128*1024 {
		return LoginCredentials{}, errors.New("invalid login credentials size")
	}
	data, err := readPrivateAccountFile(filepath.Join(directory, LoginCredentialsName))
	if os.IsNotExist(err) {
		return LoginCredentials{Email: email}, nil
	}
	if err != nil {
		return LoginCredentials{}, errors.New("cannot read login credentials")
	}
	var result LoginCredentials
	if json.Unmarshal(data, &result) != nil || result.Email != email || result.Version == 0 || result.Version > contract.MaxSafeInteger || !contract.ValidAccountID(result.AccountID) || !contract.ValidAccountID(result.LastOperationID) {
		return LoginCredentials{}, errors.New("invalid login credentials")
	}
	return result, nil
}

func WriteLoginCredentials(directory string, credentials LoginCredentials) error {
	if err := validateAccountPathAncestors(directory); err != nil {
		return errors.New("invalid login credentials directory")
	}
	path := filepath.Join(directory, LoginCredentialsName)
	if _, err := readPrivateAccountFile(path); err != nil && !os.IsNotExist(err) {
		return errors.New("invalid login credentials file")
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return errors.New("cannot encode login credentials")
	}
	if err := clearAccountWriteTemps(directory, []string{LoginCredentialsName}); err != nil {
		return errors.New("cannot clean login credentials temporary files")
	}
	if err := atomicWriteFile(path, append(data, '\n'), 0600); err != nil {
		return errors.New("cannot save login credentials")
	}
	return nil
}
