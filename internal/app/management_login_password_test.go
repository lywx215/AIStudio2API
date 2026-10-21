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

func TestLoginPasswordLifecycleAndIsolation(t *testing.T) {
	ctx := context.Background()
	coordinator, store, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	service := coordinator.(c.LoginPasswordCoordinator)
	id := first.Account.AccountID
	zero := uint64(0)
	password := "  synthetic-password with spaces  "
	input := c.LoginPasswordInput{Email: "SYNTHETIC@example.test", Password: &password, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000001"}
	status, err := service.WriteLoginPassword(ctx, id, input, false)
	if err != nil || !status.Configured || status.Version != 1 {
		t.Fatal("initial write failed", err)
	}
	if repeated, err := service.WriteLoginPassword(ctx, id, input, false); err != nil || repeated != status {
		t.Fatal("idempotent retry failed", err)
	}
	other := "different"
	conflicting := input
	conflicting.Password = &other
	if _, err := service.WriteLoginPassword(ctx, id, conflicting, false); c.PublicError(err).Code != c.CodeIdempotencyConflict {
		t.Fatal("reused operation accepted")
	}
	conflicting = input
	conflicting.OperationID = "10000000-0000-4000-8000-000000000002"
	if _, err := service.WriteLoginPassword(ctx, id, conflicting, false); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("stale write accepted")
	}
	impl := coordinator.(*managementAccountCoordinator)
	directory, _ := aistudio.AccountDirectory(impl.paths, first.Account.NativeID, false)
	stored, err := aistudio.ReadLoginCredentials(directory, first.Account.NativeID)
	if err != nil || stored.Password != password {
		t.Fatal("password whitespace or restart read changed", err)
	}
	public, _ := json.Marshal(status)
	if strings.Contains(string(public), "synthetic-password") {
		t.Fatal("status exposed password")
	}
	if current, _ := store.GetAccount(ctx, id); current.Versions != first.Account.Versions {
		t.Fatal("password changed Cookie/state versions")
	}
	replaced, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: id, Credentials: g2Credentials("replacement"), Expected: expectedAccount(first.Account)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.LoginPassword(ctx, id); err != nil || got != status {
		t.Fatal("Cookie replacement changed password", err)
	}
	exported, err := coordinator.Export(ctx, []string{id})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exported)
	if strings.Contains(string(raw), "synthetic-password") {
		t.Fatal("Cookie export exposed password")
	}
	one := uint64(1)
	clear := c.LoginPasswordInput{Email: first.Account.NativeID, ExpectedVersion: &one, OperationID: "10000000-0000-4000-8000-000000000003"}
	status, err = service.WriteLoginPassword(ctx, id, clear, true)
	if err != nil || status.Configured || status.Version != 2 {
		t.Fatal("clear lost tombstone", err)
	}
	if _, err := service.WriteLoginPassword(ctx, id, input, false); c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("clear resurrected old password")
	}
	clear.ExpectedVersion = &status.Version
	clear.OperationID = "10000000-0000-4000-8000-000000000004"
	clear.Password = &password
	if _, err := service.WriteLoginPassword(ctx, id, clear, false); err != nil {
		t.Fatal(err)
	}
	if _, err := impl.state(ctx, c.StateCommand{AccountID: id, Expected: expectedAccount(replaced.Account)}, c.MutationDelete); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, aistudio.LoginCredentialsName)); !os.IsNotExist(err) {
		t.Fatal("deleted account retained password")
	}
}

func TestLoginPasswordStatusSurvivesModelEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	directory := t.TempDir()
	email := "synthetic@example.test"
	stored := aistudio.LoginCredentials{Email: email, Password: "private-status-marker", AccountID: "10000000-0000-4000-8000-000000000001", Version: 1, LastOperationID: "10000000-0000-4000-8000-000000000002"}
	if err := aistudio.WriteLoginCredentials(directory, stored); err != nil {
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
			if !strings.Contains(string(raw), `"login_password_set":true`) || strings.Contains(string(raw), stored.Password) {
				t.Fatal("model event lost password status or exposed password")
			}
			return
		case <-ctx.Done():
			t.Fatal("account event missing")
		}
	}
}

func TestLoginPasswordRejectsWrongIdentityAndInvalidInput(t *testing.T) {
	ctx := context.Background()
	coordinator, _, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("initial")})
	if err != nil {
		t.Fatal(err)
	}
	service := coordinator.(c.LoginPasswordCoordinator)
	zero := uint64(0)
	password := "synthetic"
	input := c.LoginPasswordInput{Email: "wrong@example.test", Password: &password, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000001"}
	if _, err := service.WriteLoginPassword(ctx, first.Account.AccountID, input, false); c.PublicError(err).Code != c.CodeInvalidCredentials {
		t.Fatal("wrong identity accepted")
	}
	input.Email = first.Account.NativeID
	input.Password = nil
	if _, err := service.WriteLoginPassword(ctx, first.Account.AccountID, input, false); c.PublicError(err).Code != c.CodeInvalidRequest {
		t.Fatal("empty password accepted")
	}
}

func TestLoginPasswordSharesAccountFileLease(t *testing.T) {
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
	password := "synthetic"
	input := c.LoginPasswordInput{Email: first.Account.NativeID, Password: &password, ExpectedVersion: &zero, OperationID: "10000000-0000-4000-8000-000000000001"}
	service := coordinator.(c.LoginPasswordCoordinator)
	_, writeErr := service.WriteLoginPassword(ctx, first.Account.AccountID, input, false)
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if c.PublicError(writeErr).Code != c.CodeAccountBusy {
		t.Fatal("account lease did not fence password write", writeErr)
	}
	if _, err := service.WriteLoginPassword(ctx, first.Account.AccountID, input, false); err != nil {
		t.Fatal(err)
	}
}
