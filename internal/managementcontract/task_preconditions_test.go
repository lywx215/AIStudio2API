package managementcontract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskPreconditionMissingAndOriginalExpected(t *testing.T) {
	account := AccountRecord{Account: Account{AccountID: "ec5f01c8-1bee-43df-957b-20f857b38bda", Versions: Versions{2, 3}}}
	missing := TaskPrecondition{AccountID: account.AccountID, SnapshotKnown: true, Missing: true}
	if code := PublicError(missing.Check(account)).Code; code != CodeNotFound {
		t.Fatalf("missing rebound: %s", code)
	}
	p := TaskPrecondition{AccountID: account.AccountID, SnapshotKnown: true, Versions: account.Versions}
	if err := p.Check(account); err != nil {
		t.Fatal(err)
	}
	credential, state := uint64(4), uint64(5)
	p.ClientExpected = ExpectedVersions{Credential: &credential, State: &state}
	if code := PublicError(p.Check(account)).Code; code != CodeVersionConflict {
		t.Fatal("explicit mismatch was rebased")
	}
	account.Versions = Versions{4, 5}
	if code := PublicError(p.Check(account)).Code; code != CodeVersionConflict {
		t.Fatal("later version rescued inconsistent snapshot")
	}
	expected := p.Expected()
	*expected.Credential = 99
	if credential != 4 {
		t.Fatal("returned CAS pair aliases original client expectation")
	}
	legacy := TaskPrecondition{AccountID: account.AccountID, ClientExpected: ExpectedVersions{Credential: &credential, State: &state}}
	if err := legacy.Check(account); err != nil {
		t.Fatalf("explicit legacy replace not protected: %v", err)
	}
	legacy.ClientExpected = ExpectedVersions{}
	if code := PublicError(legacy.Check(account)).Code; code != CodeInterrupted {
		t.Fatal("unprotected legacy target accepted")
	}
}

func TestTaskWorkPrivacyAndQueuedQueryBounds(t *testing.T) {
	work := TaskWork{PrivateInput: json.RawMessage(`{"fixture-secret":"value"}`), Preconditions: []TaskPrecondition{{AccountID: "private-target"}}, PreconditionSet: &TaskPreconditionSet{FormatVersion: 1, Mode: TaskCaptureReloadAuth, TargetCount: 0}}
	raw, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "fixture-secret") {
		t.Fatal("private task work exposed")
	}
	for _, query := range []QueuedTaskQuery{{Limit: 0}, {Limit: 201}, {Limit: 1, After: &QueuedTaskRef{TaskID: "task_fixture"}}} {
		if query.Validate() == nil {
			t.Fatal("invalid queued cursor accepted")
		}
	}
	query := QueuedTaskQuery{Limit: 200, After: &QueuedTaskRef{TaskID: "task_fixture", CreatedAt: NewTimestamp(time.Now())}}
	if err := query.Validate(); err != nil {
		t.Fatal(err)
	}
}
