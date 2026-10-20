package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/config"
	contract "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func TestManagementProjectionColdReadinessAndSafeSettings(t *testing.T) {
	cfg := config.Default()
	cfg.WAABackend = config.WAABackendGo
	cfg.Proxy = "http://fixture-secret-user:fixture-secret-password@proxy.invalid"
	cfg.ProxyAPIKey = "fixture-secret-model-key"
	cfg.AdminPassword = "fixture-secret-console-password"
	manager := &runtimeManager{activeManagement: cfg}
	projection := &managementProjection{manager: manager}
	ready, err := projection.Readiness(context.Background())
	if err != nil || ready.Ready || ready.Checks == nil {
		t.Fatalf("cold readiness: %+v %v", ready, err)
	}
	manager.current = &runtimeGeneration{config: cfg}
	ready, err = projection.Readiness(context.Background())
	if err != nil || ready.Ready {
		t.Fatalf("nil-service readiness: %+v %v", ready, err)
	}
	settings, err := projection.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-secret") || strings.Contains(string(raw), "proxy") {
		t.Fatalf("secret settings projection: %s", raw)
	}
	settings.Values.UpstreamChannels[0] = "changed"
	if manager.current.config.UpstreamChannels[0] == "changed" {
		t.Fatal("settings aliases live channels")
	}
	capabilities, err := projection.Capabilities(context.Background())
	if err != nil || capabilities.SettingsWrite || capabilities.UploadReceipts || capabilities.TargetedVerification {
		t.Fatalf("unassembled capability overclaim: %+v %v", capabilities, err)
	}
	if _, err := projection.PatchSettings(context.Background(), contract.SettingsPatch{}); contract.PublicError(err).Code != contract.CodeInvalidState {
		t.Fatal("unsupported settings mutation")
	}
}

type g0NativeProof struct {
	id     string
	epoch  uint64
	active bool
}

func (p *g0NativeProof) NativeLeaseAccountID() string { return p.id }
func (p *g0NativeProof) NativeLeaseEpoch() uint64     { return p.epoch }
func (p *g0NativeProof) NativeLeaseActive() bool      { return p.active }

func TestManagementNativeMutationEpochAndNoSelfDrain(t *testing.T) {
	runtime := &managementAccountRuntime{epoch: 7}
	proof := &g0NativeProof{id: "fixture@example.invalid", epoch: 7, active: true}
	lease, err := runtime.AcquireNativeMutation(context.Background(), proof)
	if err != nil || lease.Epoch() != 7 {
		t.Fatalf("native proof: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := runtime.AcquireNativeMutation(waitCtx, proof); err != context.DeadlineExceeded {
		t.Fatalf("native commit/publication not serialized: %v", err)
	}
	// There is deliberately no admin/pool: this path cannot wait for active=0,
	// acquire the exclusive gate or touch a production worker just to enter.
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	proof.epoch = 8
	if _, err := runtime.AcquireNativeMutation(context.Background(), proof); contract.PublicError(err).Code != contract.CodeVersionConflict {
		t.Fatal("cross-generation native lease accepted")
	}
	proof.epoch = 7
	proof.active = false
	if _, err := runtime.AcquireNativeMutation(context.Background(), proof); contract.PublicError(err).Code != contract.CodeVersionConflict {
		t.Fatal("released native lease accepted")
	}
}

type g0PublicationStore struct {
	contract.AccountMetadataStore
	latest contract.AccountRecord
}

func (s *g0PublicationStore) GetAccount(context.Context, string) (contract.AccountRecord, error) {
	return s.latest, nil
}

func TestManagementStalePublicationCannotDisableNewerState(t *testing.T) {
	latest := contract.AccountRecord{Account: contract.Account{AccountID: "ec5f01c8-1bee-43df-957b-20f857b38bda", Versions: contract.Versions{CredentialVersion: 3, StateVersion: 4}}, NativeID: "fixture@example.invalid"}
	runtime := &managementAccountRuntime{epoch: 9, admin: &runtimeAdmin{}, store: &g0PublicationStore{latest: latest}}
	lease := &managementMutationLease{runtime: runtime, nativeID: latest.NativeID, native: true}
	stale := latest
	stale.StateVersion--
	// A nil pool is intentional: any attempt to publish or mark the newer
	// account unavailable would panic instead of returning the fenced conflict.
	if err := lease.Publish(context.Background(), stale); contract.PublicError(err).Code != contract.CodeVersionConflict {
		t.Fatalf("stale publication accepted: %v", err)
	}
}
