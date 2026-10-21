package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func proxyAccount(t *testing.T, coordinator c.AccountCoordinator, id string) *aistudio.Account {
	t.Helper()
	impl := coordinator.(*managementAccountCoordinator)
	directory, err := aistudio.AccountDirectory(impl.paths, id, false)
	if err != nil {
		t.Fatal(err)
	}
	account, err := aistudio.LoadAccountDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestManagementProxyImportReplaceReceiptAndOmission(t *testing.T) {
	ctx := context.Background()
	coordinator, store, _ := g2CoordinatorFixture(t)
	proxy := "socks5://fixture:synthetic@proxy.invalid:1080"
	u := &c.UploadIdentity{TaskID: "task_10000000-0000-4000-8000-000000000001", Kind: c.TaskImport}
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("one"), Proxy: &proxy, Operation: c.OperationContext{Upload: u}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Upload == nil || !first.Upload.ProxyApplied || proxyAccount(t, coordinator, first.Account.NativeID).Config.Proxy != proxy {
		t.Fatal("proxy and credentials did not commit together")
	}
	// Native configuration keeps unrelated fields and participates in proxy CAS.
	impl := coordinator.(*managementAccountCoordinator)
	config := proxyAccount(t, coordinator, first.Account.NativeID).Config
	config.Locale, config.Timezone = "fr-FR", "UTC"
	files := impl.files()
	files.NewConfig = &config
	_, err = coordinator.CommitNative(ctx, c.CommitIntent{Kind: c.MutationState, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID, Expected: expectedAccount(first.Account), CredentialDigest: first.Account.CredentialDigest}, files)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := coordinator.Patch(ctx, c.StateCommand{AccountID: first.Account.AccountID, Expected: expectedAccount(first.Account), Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	next := "socks5://fixture:another-synthetic@other.invalid:1081"
	u = &c.UploadIdentity{TaskID: "task_10000000-0000-4000-8000-000000000002", Kind: c.TaskReplace}
	replaced, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("two"), Proxy: &next, Expected: expectedAccount(disabled.Account), Operation: c.OperationContext{Upload: u}})
	if err != nil {
		t.Fatal(err)
	}
	account := proxyAccount(t, coordinator, first.Account.NativeID)
	if account.Config.Proxy != next || account.Config.Locale != "fr-FR" || account.Config.Timezone != "UTC" || account.Config.Enabled {
		t.Fatal("replacement changed unrelated settings or disabled state")
	}
	if replaced.Account.StateVersion != disabled.Account.StateVersion+1 || !replaced.Upload.ProxyApplied {
		t.Fatal("proxy change lacks versioned committed evidence")
	}
	receipt, err := store.GetUploadReceipt(ctx, *u)
	if err != nil || receipt == nil || !receipt.ProxyApplied {
		t.Fatal("durable proxy receipt missing", err)
	}
	omitted, err := coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("three"), Expected: expectedAccount(replaced.Account)})
	if err != nil {
		t.Fatal(err)
	}
	if proxyAccount(t, coordinator, first.Account.NativeID).Config.Proxy != next || omitted.Account.StateVersion != replaced.Account.StateVersion {
		t.Fatal("omission changed proxy or its state version")
	}
	public, _ := json.Marshal(receipt)
	if string(public) == "" {
		t.Fatal("receipt serialization failed")
	}
}

func TestManagementNativeProxyAdvancesCASAndRejectsStaleReplace(t *testing.T) {
	ctx := context.Background()
	coordinator, _, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("one")})
	if err != nil {
		t.Fatal(err)
	}
	impl := coordinator.(*managementAccountCoordinator)
	config := proxyAccount(t, coordinator, first.Account.NativeID).Config
	config.Proxy = "socks5://native.invalid:1080"
	files := impl.files()
	files.NewConfig = &config
	updated, err := coordinator.CommitNative(ctx, c.CommitIntent{Kind: c.MutationState, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID, Expected: expectedAccount(first.Account), CredentialDigest: first.Account.CredentialDigest}, files)
	if err != nil || updated.Account.StateVersion != first.Account.StateVersion+1 {
		t.Fatal("native proxy bypassed state CAS", err)
	}
	_, err = coordinator.Replace(ctx, c.ReplaceCommand{AccountID: first.Account.AccountID, Credentials: g2Credentials("two"), Expected: expectedAccount(first.Account)})
	if c.PublicError(err).Code != c.CodeVersionConflict {
		t.Fatal("stale replacement accepted")
	}
}

type failedProxyApply struct{ c.AccountCommitParticipant }

func (p failedProxyApply) Apply(ctx context.Context, record c.RecoveryRecord) error {
	if err := p.AccountCommitParticipant.Apply(ctx, record); err != nil {
		return err
	}
	return errors.New("synthetic failure after file apply")
}

func TestManagementProxyRollbackRestoresBothPrivateFiles(t *testing.T) {
	ctx := context.Background()
	coordinator, store, _ := g2CoordinatorFixture(t)
	before := "socks5://before.invalid:1080"
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("one"), Proxy: &before})
	if err != nil {
		t.Fatal(err)
	}
	impl := coordinator.(*managementAccountCoordinator)
	credentials := g2Credentials("two")
	material, err := coordinator.PrepareCredentials(credentials)
	if err != nil {
		t.Fatal(err)
	}
	var state aistudio.StorageState
	if err := json.Unmarshal(material.StorageState, &state); err != nil {
		t.Fatal(err)
	}
	digest, _ := aistudio.CredentialDigest(state)
	after := "socks5://after.invalid:1081"
	files := impl.files()
	files.NewState, files.ProxyOverride = &state, &after
	_, err = store.CommitAccount(ctx, c.CommitIntent{Kind: c.MutationReplace, AccountID: first.Account.AccountID, NativeID: first.Account.NativeID, Expected: expectedAccount(first.Account), CredentialChanged: true, CredentialDigest: digest, ProxyApplied: true, ProxyChanged: true}, failedProxyApply{files})
	if err == nil {
		t.Fatal("synthetic failed apply succeeded")
	}
	account := proxyAccount(t, coordinator, first.Account.NativeID)
	if account.Config.Proxy != before || account.StorageState.Cookies[0].Value != "one" {
		t.Fatal("rollback did not restore both files")
	}
	current, err := store.GetAccount(ctx, first.Account.AccountID)
	if err != nil || current.Versions != first.Account.Versions {
		t.Fatal("rollback advanced metadata", err)
	}
}
