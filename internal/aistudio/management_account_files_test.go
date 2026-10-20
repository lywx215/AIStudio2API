package aistudio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func g2FileFixture(t *testing.T) (*AccountFileParticipant, c.RecoveryRecord, StorageState, StorageState) {
	t.Helper()
	root := t.TempDir()
	auth := filepath.Join(root, "auth")
	id := "synthetic@example.test"
	before := StorageState{Cookies: []StateCookie{{Name: "SID", Value: "before", Domain: ".google.com", Path: "/", Expires: -1}}, Origins: []StorageOrigin{}}
	after := StorageState{Cookies: []StateCookie{{Name: "SID", Value: "after", Domain: ".google.com", Path: "/", Expires: -1}}, Origins: []StorageOrigin{}}
	store := NewAccountStore(auth)
	_, lease, err := store.Create(DefaultAccountConfig(id), before)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	beforeDigest, _ := CredentialDigest(before)
	afterDigest, _ := CredentialDigest(after)
	record := c.AccountRecord{Account: c.Account{AccountID: "20000000-0000-4000-8000-000000000002", Index: 1, AccountState: c.AccountState{Enabled: true}, Versions: c.Versions{CredentialVersion: 1, StateVersion: 1}}, NativeID: id, CredentialDigest: beforeDigest}
	next := record
	next.CredentialDigest = afterDigest
	next.CredentialVersion = 2
	p := &AccountFileParticipant{AuthPaths: []string{auth}, JournalDir: filepath.Join(root, "journal"), NewState: &after}
	r := c.RecoveryRecord{Plan: c.CommitPlan{Intent: c.CommitIntent{OperationID: "op_10000000-0000-4000-8000-000000000001", Kind: c.MutationReplace}, Before: &record, After: next}, Phase: c.CommitPlanned}
	return p, r, before, after
}

func TestManagementFilePartialApplyRollbackAndRepeat(t *testing.T) {
	p, r, before, _ := g2FileFixture(t)
	ctx := context.Background()
	ref, err := p.Prepare(ctx, r.Plan)
	if err != nil {
		t.Fatal(err)
	}
	r.ParticipantRef = ref
	r.Phase = c.CommitPrepared
	journal, _, err := p.load(r)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process crash between writes, without returning participantRef
	// to the metadata layer. Planned recovery must locate the journal by op ID.
	for _, image := range journal.Files {
		if image.Name == storageStateName {
			if err := writeImage(filepath.Join(journal.Directory, image.Name), image.After, image.AfterExists); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.ParticipantRef = ""
	r.Phase = c.CommitPlanned
	if err := p.Rollback(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStorageState(filepath.Join(journal.Directory, storageStateName))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cookies, before.Cookies) {
		t.Fatal("rollback did not restore original credentials")
	}
	if err := p.Rollback(ctx, r); err != nil {
		t.Fatal("repeated rollback", err)
	}
}

func TestManagementFileApplyFinalizeRepeat(t *testing.T) {
	p, r, _, after := g2FileFixture(t)
	ctx := context.Background()
	var err error
	r.ParticipantRef, err = p.Prepare(ctx, r.Plan)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = c.CommitPrepared
	if err := p.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(ctx, r); err != nil {
		t.Fatal("repeated apply", err)
	}
	r.Phase = c.CommitCommitted
	if err := p.Finalize(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := p.Finalize(ctx, r); err != nil {
		t.Fatal("repeated finalize", err)
	}
	dir, err := AccountDirectory(p.AuthPaths, r.Plan.After.NativeID, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadStorageState(filepath.Join(dir, storageStateName))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cookies, after.Cookies) {
		t.Fatal("committed image lost")
	}
}

func TestManagementRecoveryRefusesNewerCredentials(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "finalize"}[committed], func(t *testing.T) {
			p, r, _, after := g2FileFixture(t)
			ctx := context.Background()
			var err error
			r.ParticipantRef, err = p.Prepare(ctx, r.Plan)
			if err != nil {
				t.Fatal(err)
			}
			r.Phase = c.CommitPrepared
			if err := p.Apply(ctx, r); err != nil {
				t.Fatal(err)
			}
			dir, err := AccountDirectory(p.AuthPaths, r.Plan.After.NativeID, false)
			if err != nil {
				t.Fatal(err)
			}
			after.Cookies[0].Value = "newer-manual-replacement"
			if err := WriteStorageState(filepath.Join(dir, storageStateName), after); err != nil {
				t.Fatal(err)
			}
			if committed {
				r.Phase = c.CommitCommitted
				err = p.Finalize(ctx, r)
			} else {
				err = p.Rollback(ctx, r)
			}
			if err == nil {
				t.Fatal("recovery accepted unexpected newer file")
			}
			got, err := LoadStorageState(filepath.Join(dir, storageStateName))
			if err != nil {
				t.Fatal(err)
			}
			if got.Cookies[0].Value != "newer-manual-replacement" {
				t.Fatal("newer file overwritten")
			}
		})
	}
}

func TestManagementDeleteRollbackRestoresFingerprint(t *testing.T) {
	p, r, before, _ := g2FileFixture(t)
	ctx := context.Background()
	p.NewState = nil
	r.Plan.Intent.Kind = c.MutationDelete
	r.Plan.After.Deleted = true
	r.Plan.After.CredentialDigest = r.Plan.Before.CredentialDigest
	dir, err := AccountDirectory(p.AuthPaths, r.Plan.After.NativeID, false)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := []byte(`{"synthetic":true}`)
	if err := os.WriteFile(filepath.Join(dir, "camoufox-fingerprint.json"), fingerprint, 0600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "camoufox-cache", "entries")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "synthetic-cache"), []byte("cache-before-delete"), 0600); err != nil {
		t.Fatal(err)
	}
	r.ParticipantRef, err = p.Prepare(ctx, r.Plan)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = c.CommitPrepared
	if err := p.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, storageStateName)); !os.IsNotExist(err) {
		t.Fatal("delete did not remove credential file")
	}
	if err := p.Rollback(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStorageState(filepath.Join(dir, storageStateName))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cookies, before.Cookies) {
		t.Fatal("delete rollback lost credentials")
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "camoufox-fingerprint.json"))
	if err != nil || !reflect.DeepEqual(bytes, fingerprint) {
		t.Fatal("delete rollback lost fingerprint")
	}
	cacheBytes, err := os.ReadFile(filepath.Join(cache, "synthetic-cache"))
	if err != nil || string(cacheBytes) != "cache-before-delete" {
		t.Fatal("delete rollback lost nested browser cache", err)
	}
}

func TestManagementCookieThreeWayMergePreservesNewerValues(t *testing.T) {
	base := []StateCookie{{Name: "SID", Value: "original", Domain: ".google.com", Path: "/"}, {Name: "OLD", Value: "old", Domain: ".google.com", Path: "/"}}
	incoming := append([]StateCookie(nil), base...)
	incoming[0].Value = "stale-response"
	latest := append([]StateCookie(nil), base...)
	latest[0].Value = "newer-response"
	latest = append(latest, StateCookie{Name: "FRESH", Value: "keep", Domain: ".google.com", Path: "/"})
	got := mergeCookieChanges(base, incoming, latest)
	if !reflect.DeepEqual(got, latest) {
		t.Fatal("older response overwrote a concurrent cookie")
	}
	incoming[1].Value = "independent-change"
	got = mergeCookieChanges(base, incoming, latest)
	if got[0].Value != "newer-response" || got[1].Value != "independent-change" || len(got) != 3 {
		t.Fatal("independent merge lost latest file")
	}
}

func TestManagementDigestIsCanonicalAndNativeExtensionsMatter(t *testing.T) {
	var left, right StorageState
	if err := json.Unmarshal([]byte(`{"cookies":[],"origins":[],"synthetic":{"x":1}}`), &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte("{\n\"origins\":[],\"synthetic\":{\"x\":1},\"cookies\":[]}"), &right); err != nil {
		t.Fatal(err)
	}
	a, _ := CredentialDigest(left)
	b, _ := CredentialDigest(right)
	if a != b {
		t.Fatal("whitespace affected digest")
	}
	delete(right.extra, "synthetic")
	b, _ = CredentialDigest(right)
	if a == b {
		t.Fatal("native extension omitted from digest")
	}
}

func TestManagementRecoveryCleansOnlyOperationTemps(t *testing.T) {
	p, r, _, _ := g2FileFixture(t)
	ctx := context.Background()
	if err := os.MkdirAll(p.JournalDir, 0700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(p.JournalDir, "."+r.Plan.Intent.OperationID+".json-crash.tmp")
	unrelated := filepath.Join(p.JournalDir, ".op_unrelated.json-crash.tmp")
	for _, path := range []string{orphan, unrelated} {
		if err := os.WriteFile(path, []byte("synthetic staged credential"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Rollback(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("planned rollback left its staged credential")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("rollback removed another operation's file")
	}
	r.ParticipantRef, _ = p.Prepare(ctx, r.Plan)
	r.Phase = c.CommitPrepared
	if err := p.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	dir, err := AccountDirectory(p.AuthPaths, r.Plan.After.NativeID, false)
	if err != nil {
		t.Fatal(err)
	}
	targetTemp := filepath.Join(dir, "."+storageStateName+"-crash.tmp")
	if err := os.WriteFile(targetTemp, []byte("synthetic staged credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.Rollback(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(targetTemp); !os.IsNotExist(err) {
		t.Fatal("rollback left its temporary credential write")
	}
}

func TestManagementCommittedDeleteCanRecoverAfterDirectoryRemoval(t *testing.T) {
	p, r, _, _ := g2FileFixture(t)
	ctx := context.Background()
	p.NewState = nil
	r.Plan.Intent.Kind = c.MutationDelete
	r.Plan.After.Deleted = true
	r.Plan.After.CredentialDigest = r.Plan.Before.CredentialDigest
	var err error
	r.ParticipantRef, err = p.Prepare(ctx, r.Plan)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = c.CommitPrepared
	if err := p.Apply(ctx, r); err != nil {
		t.Fatal(err)
	}
	journal, ref, err := p.load(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(journal.BackupDirectory); err != nil {
		t.Fatal(err)
	}
	// Simulate Finalize crashing after the account directory was removed but
	// before its committed journal was unlinked.
	r.Phase = c.CommitCommitted
	if err := p.Finalize(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ref); !os.IsNotExist(err) {
		t.Fatal("committed deletion journal not cleaned")
	}
	if err := p.Finalize(ctx, r); err != nil {
		t.Fatal("repeated deleted finalize", err)
	}
}

func TestManagementPathsRejectSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	alias := filepath.Join(t.TempDir(), "auth-link")
	if err := os.Symlink(outside, alias); err != nil {
		t.Skipf("test filesystem does not permit symlink creation: %v", err)
	}
	if _, err := AccountDirectory([]string{alias}, "synthetic@example.test", false); err == nil {
		t.Fatal("account resolver accepted a symlink ancestor")
	}
	p, r, _, _ := g2FileFixture(t)
	directory, err := AccountDirectory(p.AuthPaths, r.Plan.After.NativeID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "camoufox-cache")); err != nil {
		t.Fatal(err)
	}
	p.NewState = nil
	r.Plan.Intent.Kind = c.MutationDelete
	r.Plan.After.Deleted = true
	r.Plan.After.CredentialDigest = r.Plan.Before.CredentialDigest
	if _, err := p.Prepare(context.Background(), r.Plan); err == nil {
		t.Fatal("delete preparation accepted cache reparse escape")
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatal("rejected preparation changed original account", err)
	}
}
