package managementstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

var testTime = time.Date(2026, 10, 20, 1, 2, 3, 456000000, time.UTC)

func openTest(t *testing.T, path string) *store {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "metadata # space.db")
	}
	api, err := Open(context.Background(), c.StoreOptions{Path: path, Clock: func() time.Time { return testTime }})
	if err != nil {
		t.Fatal(err)
	}
	s := api.(*store)
	t.Cleanup(func() { s.Close() })
	return s
}
func id(t *testing.T, prefix string) string {
	t.Helper()
	v, err := c.NewID(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func assertCode(t *testing.T, err error, code c.ErrorCode) {
	t.Helper()
	var e *c.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error=%v; want %s", err, code)
	}
}
func createTestKey(t *testing.T, s *store) c.KeyCreated {
	t.Helper()
	k, err := s.CreateKey(context.Background(), c.KeyCreateRequest{Name: "Synthetic operator", Scopes: c.ScopeTemplate("admin")})
	if err != nil {
		t.Fatal(err)
	}
	return k
}
func admitTest(t *testing.T, s *store, k c.KeyCreated, key string, kind c.TaskKind) c.TaskRecord {
	t.Helper()
	a := testAdmission(k, key, kind)
	result, err := s.AdmitTask(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.GetTask(context.Background(), result.Accepted.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func testAdmission(k c.KeyCreated, key string, kind c.TaskKind) c.Admission {
	item := c.TaskItem{ClientRef: "synthetic-item"}
	if captureMode(kind) == c.TaskCaptureItems {
		item.AccountID = mustID("")
	}
	return c.Admission{Principal: c.Principal{KeyID: k.Key.ID}, IdempotencyKey: key, CanonicalDigest: sha256.Sum256([]byte("POST /synthetic " + key)), Kind: kind, Items: []c.TaskItem{item}, PrivateInput: json.RawMessage(`{"syntheticSecret":"private-cookie-marker"}`), QueueLimit: 100, Capture: c.TaskCaptureInstruction{Mode: captureMode(kind)}}
}

type participantImage struct {
	Before  []byte
	Existed bool
	After   []byte
}

// This fake participant uses real temporary files and a separate durable journal.
// It models interrupted Prepare/Apply/Finalize without any native account files.
type fileParticipant struct {
	root  string
	after []byte
	crash string
	fail  string
	probe func(context.Context) error
	mu    sync.Mutex
	calls map[string]int
}

func newParticipant(t *testing.T) *fileParticipant {
	return &fileParticipant{root: t.TempDir(), after: []byte("synthetic-new"), calls: map[string]int{}}
}
func (p *fileParticipant) call(name string) error {
	p.mu.Lock()
	p.calls[name]++
	p.mu.Unlock()
	if p.crash == name {
		panic("simulated crash")
	}
	if p.fail == name {
		return errors.New("private-cookie-marker must not escape")
	}
	return nil
}
func (p *fileParticipant) target() string           { return filepath.Join(p.root, "synthetic-account") }
func (p *fileParticipant) journal(op string) string { return filepath.Join(p.root, op+".json") }
func (p *fileParticipant) Prepare(ctx context.Context, plan c.CommitPlan) (string, error) {
	if p.probe != nil {
		if err := p.probe(ctx); err != nil {
			return "", err
		}
	}
	b, err := os.ReadFile(p.target())
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	image := participantImage{Before: b, Existed: exists, After: p.after}
	data, err := json.Marshal(image)
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(p.journal(plan.Intent.OperationID), data, 0600); err != nil {
		return "", err
	}
	if err = p.call("prepare"); err != nil {
		return "", err
	}
	return plan.Intent.OperationID, nil
}
func (p *fileParticipant) Apply(ctx context.Context, r c.RecoveryRecord) error {
	if p.probe != nil {
		if err := p.probe(ctx); err != nil {
			return err
		}
	}
	b, err := os.ReadFile(p.journal(r.Plan.Intent.OperationID))
	if err != nil {
		return err
	}
	var image participantImage
	if err = json.Unmarshal(b, &image); err != nil {
		return err
	}
	if err = os.WriteFile(p.target(), image.After, 0600); err != nil {
		return err
	}
	return p.call("apply")
}
func (p *fileParticipant) Rollback(ctx context.Context, r c.RecoveryRecord) error {
	if p.probe != nil {
		if err := p.probe(ctx); err != nil {
			return err
		}
	}
	if err := p.call("rollback"); err != nil {
		return err
	}
	b, err := os.ReadFile(p.journal(r.Plan.Intent.OperationID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var image participantImage
	if err = json.Unmarshal(b, &image); err != nil {
		return err
	}
	if image.Existed {
		err = os.WriteFile(p.target(), image.Before, 0600)
	} else {
		err = os.Remove(p.target())
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	return os.Remove(p.journal(r.Plan.Intent.OperationID))
}
func (p *fileParticipant) Finalize(ctx context.Context, r c.RecoveryRecord) error {
	if p.probe != nil {
		if err := p.probe(ctx); err != nil {
			return err
		}
	}
	if err := p.call("finalize"); err != nil {
		return err
	}
	err := os.Remove(p.journal(r.Plan.Intent.OperationID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func importTest(t *testing.T, s *store, p *fileParticipant, native string, upload *c.UploadIdentity) c.AccountCommitResult {
	t.Helper()
	result, err := s.CommitAccount(context.Background(), c.CommitIntent{OperationID: id(t, "op"), Kind: c.MutationImport, NativeID: native, CredentialDigest: sha256.Sum256(p.after), Upload: upload}, p)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func expected(a c.AccountRecord) c.ExpectedVersions {
	cv, sv := a.CredentialVersion, a.StateVersion
	return c.ExpectedVersions{Credential: &cv, State: &sv}
}
func captureCrash(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected simulated crash")
		}
	}()
	fn()
}
