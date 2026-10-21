package aistudio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (s *AccountStore) LoadLoginTotpCredentials(ctx context.Context, account *Account) (LoginTotpCredentials, error) {
	stored, err := ReadLoginTotpCredentials(account.Directory, account.ID)
	if err != nil || stored.Version == 0 {
		return stored, err
	}
	if s.writer != nil {
		record, err := s.writer.SnapshotNative(ctx, account.ID)
		if err != nil || record.Deleted || record.AccountID != stored.AccountID {
			return LoginTotpCredentials{}, errors.New("login credentials identity mismatch")
		}
	}
	return stored, nil
}

const LoginTotpCredentialsName = "login-totp.json"

// LoginTotpCredentials is deliberately separate from exported Cookie credentials.
// The TOTP secret is stored verbatim, as requested by the account owner.
type LoginTotpCredentials struct {
	Email           string `json:"email"`
	Secret          string `json:"twofa_secret"`
	AccountID       string `json:"accountId"`
	Version         uint64 `json:"version"`
	LastOperationID string `json:"lastOperationId"`
}

func (LoginTotpCredentials) String() string   { return "[login credentials]" }
func (LoginTotpCredentials) GoString() string { return "[login credentials]" }

func (c LoginTotpCredentials) Status() contract.LoginTotpStatus {
	return contract.LoginTotpStatus{Configured: c.Secret != "", Version: c.Version, LastOperationID: c.LastOperationID}
}

func ReadLoginTotpCredentials(directory, email string) (LoginTotpCredentials, error) {
	if err := validateAccountPathAncestors(directory); err != nil {
		return LoginTotpCredentials{}, errors.New("invalid login credentials directory")
	}
	if info, err := os.Lstat(filepath.Join(directory, LoginTotpCredentialsName)); err == nil && info.Size() > 128*1024 {
		return LoginTotpCredentials{}, errors.New("invalid login credentials size")
	}
	data, err := readPrivateAccountFile(filepath.Join(directory, LoginTotpCredentialsName))
	if os.IsNotExist(err) {
		return LoginTotpCredentials{Email: email}, nil
	}
	if err != nil {
		return LoginTotpCredentials{}, errors.New("cannot read login credentials")
	}
	var result LoginTotpCredentials
	if json.Unmarshal(data, &result) != nil || result.Email != email || result.Version == 0 || result.Version > contract.MaxSafeInteger || !contract.ValidAccountID(result.AccountID) || !contract.ValidAccountID(result.LastOperationID) {
		return LoginTotpCredentials{}, errors.New("invalid login credentials")
	}
	return result, nil
}

func WriteLoginTotpCredentials(directory string, credentials LoginTotpCredentials) error {
	if err := validateAccountPathAncestors(directory); err != nil {
		return errors.New("invalid login credentials directory")
	}
	path := filepath.Join(directory, LoginTotpCredentialsName)
	if _, err := readPrivateAccountFile(path); err != nil && !os.IsNotExist(err) {
		return errors.New("invalid login credentials file")
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return errors.New("cannot encode login credentials")
	}
	if err := clearAccountWriteTemps(directory, []string{LoginTotpCredentialsName}); err != nil {
		return errors.New("cannot clean login credentials temporary files")
	}
	if err := atomicWriteFile(path, append(data, '\n'), 0600); err != nil {
		return errors.New("cannot save login credentials")
	}
	return nil
}
