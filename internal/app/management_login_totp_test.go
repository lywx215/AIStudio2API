package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestLoginTotpLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	coordinator, store, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	service := coordinator.(c.LoginTotpCoordinator)
	id := first.Account.AccountID
	zero := uint64(0)
	pw := "unchanged login password"
	passwordStatus, err := coordinator.(c.LoginPasswordCoordinator).WriteLoginPassword(ctx, id, c.LoginPasswordInput{Email: first.Account.NativeID, Password: &pw, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000009"}, false)
	if err != nil {
		t.Fatal(err)
	}
	password := "gezd-gnbv gy3tqojqgezdgnbvgy3tqojq"
	input := c.LoginTotpInput{Email: "SYNTHETIC@example.test", Secret: &password, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000001"}
	status, err := service.WriteLoginTotp(ctx, id, input, false)
	if err != nil || !status.Configured || status.Version != 1 {
		t.Fatal("initial write failed", err)
	}
	if repeated, err := service.WriteLoginTotp(ctx, id, input, false); err != nil || repeated != status {
		t.Fatal("idempotent retry failed", err)
	}
	other := "JBSWY3DPEHPK3PXP"
	conflicting := input
	conflicting.Secret = &other
	if _, err := service.WriteLoginTotp(ctx, id, conflicting, false); c.PublicError(err).Code != c.CodeIdempotencyConflict {
		t.Fatal("reused operation accepted")
	}
	conflicting = input
	conflicting.OperationID = "10000000-0000-4000-8000-000000000002"
	if _, err := service.WriteLoginTotp(ctx, id, conflicting, false); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("stale write accepted")
	}
	impl := coordinator.(*managementAccountCoordinator)
	directory, _ := aistudio.AccountDirectory(impl.paths, first.Account.NativeID, false)
	stored, err := aistudio.ReadLoginTotpCredentials(directory, first.Account.NativeID)
	if err != nil || stored.Secret != password {
		t.Fatal("password whitespace or restart read changed", err)
	}
	public, _ := json.Marshal(status)
	if strings.Contains(string(public), password) {
		t.Fatal("status exposed password")
	}
	if current, _ := store.GetAccount(ctx, id); current.Versions != first.Account.Versions {
		t.Fatal("password changed Cookie/state versions")
	}
	replaced, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: id, Credentials: g2Credentials("replacement"), Expected: expectedAccount(first.Account)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.LoginTotp(ctx, id); err != nil || got != status {
		t.Fatal("Cookie replacement changed password", err)
	}
	archived, err := coordinator.Archive(ctx, c.StateCommand{AccountID: id, Expected: expectedAccount(replaced.Account)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.LoginTotp(ctx, id); err != nil || got != status {
		t.Fatal("archive lost TOTP", err)
	}
	replaced, err = coordinator.Restore(ctx, c.StateCommand{AccountID: id, Expected: expectedAccount(archived.Account)})
	if err != nil {
		t.Fatal(err)
	}
	exported, err := coordinator.Export(ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exported)
	if strings.Contains(string(raw), password) {
		t.Fatal("Cookie export exposed password")
	}
	one := uint64(1)
	clear := c.LoginTotpInput{Email: first.Account.NativeID, ExpectedVersion: &one, OperationID: "10000000-0000-4000-8000-000000000003"}
	status, err = service.WriteLoginTotp(ctx, id, clear, true)
	if err != nil || status.Configured || status.Version != 2 {
		t.Fatal("clear lost tombstone", err)
	}
	if got, err := coordinator.(c.LoginPasswordCoordinator).LoginPassword(ctx, id); err != nil || got != passwordStatus {
		t.Fatal("TOTP changed password version", err)
	}
	if stored, err := aistudio.ReadLoginCredentials(directory, first.Account.NativeID); err != nil || stored.Password != pw {
		t.Fatal("TOTP changed password", err)
	}
	if _, err := service.WriteLoginTotp(ctx, id, input, false); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("clear resurrected old password")
	}
	clear.ExpectedVersion = &status.Version
	clear.OperationID = "10000000-0000-4000-8000-000000000004"
	clear.Secret = &password
	if _, err := service.WriteLoginTotp(ctx, id, clear, false); err != nil {
		t.Fatal(err)
	}
	if _, err := impl.state(ctx, c.StateCommand{AccountID: id, Expected: expectedAccount(replaced.Account)}, c.MutationDelete); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, aistudio.LoginTotpCredentialsName)); !os.IsNotExist(err) {
		t.Fatal("deleted account retained password")
	}
}

func TestLoginTotpStatusSurvivesModelEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	directory := t.TempDir()
	email := "synthetic@example.test"
	stored := aistudio.LoginTotpCredentials{Email: email, Secret: "JBSWY3DPEHPK3PXP", AccountID: "10000000-0000-4000-8000-000000000001", Version: 1, LastOperationID: "10000000-0000-4000-8000-000000000002"}
	if err := aistudio.WriteLoginTotpCredentials(directory, stored); err != nil {
		t.Fatal(err)
	}
	account := &aistudio.Account{ID: email, Directory: directory, Config: aistudio.DefaultAccountConfig(email), State: aistudio.AccountReady}
	registry := newRequestRegistry(ctx)
	subscriber := registry.subscribe(ctx)
	registry.activateSubscriber(subscriber, nil, nil)
	service := &trackedService{lifecycle: ctx, pool: aistudio.NewAccountPool([]*aistudio.Account{account}, 1), requests: registry}
	service.publishModelAccess()
	for {
		select {
		case event := <-subscriber.events:
			if event.Type != "accounts" {
				continue
			}
			raw, _ := json.Marshal(event.Data)
			if !strings.Contains(string(raw), `"login_totp_set":true`) || strings.Contains(string(raw), stored.Secret) {
				t.Fatal("model event lost password status or exposed password")
			}
			return
		case <-ctx.Done():
			t.Fatal("account event missing")
		}
	}
}

func TestLoginTotpRejectsWrongIdentityAndInvalidInput(t *testing.T) {
	ctx := context.Background()
	coordinator, _, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	service := coordinator.(c.LoginTotpCoordinator)
	zero := uint64(0)
	password := "JBSWY3DPEHPK3PXP"
	input := c.LoginTotpInput{Email: "wrong@example.test", Secret: &password, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000001"}
	if _, err := service.WriteLoginTotp(ctx, first.Account.AccountID, input, false); c.PublicError(err).Code != c.CodeInvalidCredentials {
		t.Fatal("wrong identity accepted")
	}
	input.Email = first.Account.NativeID
	input.Secret = nil
	if _, err := service.WriteLoginTotp(ctx, first.Account.AccountID, input, false); c.PublicError(err).Code != c.CodeInvalidRequest {
		t.Fatal("empty password accepted")
	}
	for _, invalid := range []string{"123456", "invalid!", "", "otpauth://totp/test"} {
		input.Secret = &invalid
		if _, err := service.WriteLoginTotp(ctx, first.Account.AccountID, input, false); c.PublicError(err).Code != c.CodeInvalidRequest {
			t.Fatal("invalid TOTP accepted")
		}
	}
}

func TestLoginTotpSharesAccountFileLease(t *testing.T) {
	ctx := context.Background()
	coordinator, _, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	impl := coordinator.(*managementAccountCoordinator)
	directory, _ := aistudio.AccountDirectory(impl.paths, first.Account.NativeID, false)
	release, err := aistudio.AcquireLoginCredentialsLease(directory)
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	password := "JBSWY3DPEHPK3PXP"
	input := c.LoginTotpInput{Email: first.Account.NativeID, Secret: &password, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000001"}
	service := coordinator.(c.LoginTotpCoordinator)
	_, writeErr := service.WriteLoginTotp(ctx, first.Account.AccountID, input, false)
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if c.PublicError(writeErr).Code != c.CodeAccountBusy {
		t.Fatal("account lease did not fence password write", writeErr)
	}
	if _, err := service.WriteLoginTotp(ctx, first.Account.AccountID, input, false); err != nil {
		t.Fatal(err)
	}
}
