package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	p "github.com/Mag1cFall/AIStudio2API/internal/proxymanager"
)

func TestConsoleProxyJobRealCoordinatorPreservesCredentialAndDisabledState(t *testing.T) {
	ctx := context.Background()
	coordinator, _, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("synthetic-cookie")})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := coordinator.Patch(ctx, c.StateCommand{AccountID: first.Account.AccountID, Expected: expectedAccount(first.Account), Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	backend := &proxyAccountBackend{coordinator: coordinator.(*managementAccountCoordinator)}
	service, err := p.Open(ctx, p.Options{Path: filepath.Join(t.TempDir(), "proxies.db"), Accounts: backend, Download: func(context.Context, string) ([]byte, error) {
		return []byte("proxy.invalid:1080:fixture:synthetic-password"), nil
	}, Probe: func(context.Context, string) p.ProbeResult {
		return p.ProbeResult{State: "passed", ExitIP: "8.8.8.8", LatencyMS: 10}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	source, err := service.SaveSource(ctx, "", p.SourceInput{Name: "Synthetic", Protocol: "socks5", URL: "https://example.test/synthetic-source-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Refresh(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	await := func(id string) p.Job {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			j, err := service.Job(ctx, id, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status != "queued" && j.Status != "running" {
				return j
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("timeout")
		return p.Job{}
	}
	probe, err := service.Submit(ctx, "synthetic-probe", p.JobInput{Kind: "test", All: true})
	if err != nil {
		t.Fatal(err)
	}
	await(probe.ID)
	before, err := backend.Snapshot(ctx, first.Account.NativeID)
	if err != nil {
		t.Fatal(err)
	}
	job, err := service.Submit(ctx, "synthetic-assignment", p.JobInput{Kind: "assign", Mode: "random", Targets: []p.Target{{ID: before.ID, CredentialVersion: before.CredentialVersion, StateVersion: before.StateVersion}}})
	if err != nil {
		t.Fatal(err)
	}
	result := await(job.ID)
	if len(result.Items) != 1 || result.Items[0].Status != "succeeded" || result.Items[0].Receipt == nil {
		t.Fatal(result)
	}
	account := proxyAccount(t, coordinator, first.Account.NativeID)
	if account.Config.Enabled || account.StorageState.Cookies[0].Value != "synthetic-cookie" || !strings.HasPrefix(account.Config.Proxy, "socks5://") {
		t.Fatal("proxy-only job changed credentials/settings")
	}
	after, _ := backend.Snapshot(ctx, before.ID)
	if after.CredentialVersion != disabled.Account.CredentialVersion || after.StateVersion != disabled.Account.StateVersion+1 {
		t.Fatal("incorrect proxy-only versions")
	}
	data, _ := json.Marshal([]any{after, result})
	if strings.Contains(string(data), "synthetic-password") || strings.Contains(string(data), "synthetic-source-secret") || strings.Contains(string(data), "socks5://") {
		t.Fatal("public result leaked private data")
	}
	stale, err := service.Submit(ctx, "synthetic-stale", p.JobInput{Kind: "assign", Mode: "random", Targets: []p.Target{{ID: before.ID, CredentialVersion: before.CredentialVersion, StateVersion: before.StateVersion}}})
	if err != nil {
		t.Fatal(err)
	}
	if await(stale.ID).Items[0].Code != "VERSION_CONFLICT" {
		t.Fatal("stale version accepted")
	}
}

func TestProxyReadbackRequiresOriginalCommittedOperation(t *testing.T) {
	ctx := context.Background()
	coordinator, _, _ := g2CoordinatorFixture(t)
	first, err := coordinator.Import(ctx, c.ImportCommand{Credentials: g2Credentials("one")})
	if err != nil {
		t.Fatal(err)
	}
	backend := &proxyAccountBackend{coordinator: coordinator.(*managementAccountCoordinator)}
	snapshot, _ := backend.Snapshot(ctx, first.Account.NativeID)
	op, _ := c.NewID("op")
	if _, found, err := backend.Committed(ctx, op); err != nil || found {
		t.Fatal("invented commit")
	}
	receipt, err := backend.Apply(ctx, p.Work{Account: snapshot, OperationID: op, URL: "socks5://fixture:synthetic-password@proxy.invalid:1080"})
	if err != nil {
		t.Fatal(err)
	}
	proof, found, err := backend.Committed(ctx, op)
	if err != nil || !found || proof != receipt {
		t.Fatal("missing original proxy commit")
	}
	later, _ := coordinator.Patch(ctx, c.StateCommand{AccountID: first.Account.AccountID, Expected: c.ExpectedVersions{Credential: &receipt.CredentialVersion, State: &receipt.StateVersion}, Enabled: false})
	proof, found, err = backend.Committed(ctx, op)
	if err != nil || !found || proof.StateVersion == later.Account.StateVersion {
		t.Fatal("receipt followed live version")
	}
}
