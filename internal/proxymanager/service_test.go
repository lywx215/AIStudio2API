package proxymanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, backend AccountBackend) (*Service, *[]byte) {
	t.Helper()
	data := []byte("proxy.invalid:1080:synthetic-user:p@ss:word\nother.invalid:1081:synthetic-user:password\n")
	s, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "private.db"), Accounts: backend, Download: func(context.Context, string) ([]byte, error) { return data, nil }, Probe: func(context.Context, string) ProbeResult {
		return ProbeResult{State: "passed", ExitIP: "8.8.8.8", LatencyMS: 12, StudioLatencyMS: 25}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, &data
}
func source(t *testing.T, s *Service) Source {
	t.Helper()
	v, err := s.SaveSource(context.Background(), "", SourceInput{Name: "Synthetic", Kind: "webshare", Protocol: "socks5", URL: "https://example.test/private-synthetic-token", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func refresh(t *testing.T, s *Service, id string) {
	t.Helper()
	if _, _, err := s.Refresh(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}
func waitJob(t *testing.T, s *Service, id string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, err := s.Job(context.Background(), id, 0, 200)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != "queued" && j.Status != "running" {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}
func healthy(t *testing.T, s *Service) {
	t.Helper()
	j, err := s.Submit(context.Background(), newID(), JobInput{Kind: "test", All: true})
	if err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, s, j.ID)
	for _, v := range job.Items {
		if v.Status != "succeeded" {
			t.Fatal(v.Code)
		}
	}
}
func TestParseEncodingDedupAndBounds(t *testing.T) {
	values, invalid, err := Parse([]byte("proxy.invalid:1080:u@name:p:/@word\nproxy.invalid:1080:u@name:p:/@word\nbroken\nsocks5://u%40name:p%3A%2F%40word@proxy.invalid:1080\n"), "socks5")
	if err != nil || invalid != 1 || len(values) != 1 {
		t.Fatal(values, invalid, err)
	}
	if !strings.Contains(values[0], "u%40name:p%3A%2F%40word@") {
		t.Fatal("authentication not encoded")
	}
	for _, value := range []string{"127.0.0.1:1080", "socks5h://proxy.invalid:1080", "socks5://proxy.invalid:65536", "socks5://[::1]:1080", "socks5://bad\\host:1080", "socks5://:password@proxy.invalid:1080"} {
		if _, _, err := Parse([]byte(value), "socks5"); err == nil {
			t.Fatal("invalid proxy accepted")
		}
	}
	if _, _, err := Parse(make([]byte, (5<<20)+1), "socks5"); err == nil {
		t.Fatal("oversize accepted")
	}
}
func TestSourceRefreshCASPreservesHealthAndRemovals(t *testing.T) {
	s, data := fixture(t, nil)
	v := source(t, s)
	refresh(t, s, v.ID)
	healthy(t, s)
	page, err := s.List(context.Background(), Query{})
	if err != nil || page.Total != 2 || !page.Items[0].Eligible {
		t.Fatal(page, err)
	}
	stats, err := s.Stats(context.Background())
	if err != nil || stats.Passed != 2 || stats.Eligible != 2 {
		t.Fatal(stats, err)
	}
	*data = []byte("proxy.invalid:1080:synthetic-user:p@ss:word\n")
	refresh(t, s, v.ID)
	removed, err := s.List(context.Background(), Query{State: "removed"})
	if err != nil || removed.Total != 1 || removed.Items[0].Eligible {
		t.Fatal(removed, err)
	}
	before, _ := s.List(context.Background(), Query{Eligible: true})
	*data = []byte("invalid")
	if _, _, err = s.Refresh(context.Background(), v.ID); Code(err) != "EMPTY_PROXY_LIST" {
		t.Fatal(err)
	}
	after, _ := s.List(context.Background(), Query{Eligible: true})
	if before.Total != after.Total {
		t.Fatal("failed refresh changed catalog")
	}
	sources, _ := s.Sources(context.Background())
	edited := sources[0]
	if _, err = s.SaveSource(context.Background(), v.ID, SourceInput{Name: "Edited", Protocol: "socks5", Enabled: true, Revision: v.Revision}); Code(err) != "VERSION_CONFLICT" {
		t.Fatal("stale source accepted")
	}
	if _, err = s.SaveSource(context.Background(), v.ID, SourceInput{Name: "Edited", Protocol: "socks5", Enabled: true, Revision: edited.Revision}); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal([]any{page, after, sources})
	for _, secret := range []string{"private-synthetic-token", "synthetic-user", "p@ss", "socks5://"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("public catalog leaked private input")
		}
	}
}
func TestFiltersAndExpiredHealth(t *testing.T) {
	s, _ := fixture(t, nil)
	v := source(t, s)
	refresh(t, s, v.ID)
	healthy(t, s)
	page, _ := s.List(context.Background(), Query{Search: "proxy.invalid", Protocol: "socks5", SourceID: v.ID, Limit: 1})
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatal(page)
	}
	p := page.Items[0]
	if err := s.SetEnabled(context.Background(), p.ID, false, p.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled(context.Background(), p.ID, true, p.Revision); Code(err) != "VERSION_CONFLICT" {
		t.Fatal(err)
	}
	_, _ = s.db.Exec("UPDATE proxies SET checked_at=?", time.Now().Add(-25*time.Hour).UTC().Format(time.RFC3339Nano))
	eligible, _ := s.List(context.Background(), Query{Eligible: true})
	if eligible.Total != 0 {
		t.Fatal("expired probes eligible")
	}
	if _, err := s.List(context.Background(), Query{Limit: 201}); err == nil {
		t.Fatal("unbounded page accepted")
	}
}

type fakeAccounts struct {
	mu       sync.Mutex
	accounts map[string]Account
	commits  map[string]Receipt
	calls    int
	unknown  bool
	lost     bool
}

func fakeBackend() *fakeAccounts {
	return &fakeAccounts{accounts: map[string]Account{"one": {ID: "one", UUID: "synthetic-one", Enabled: false, CredentialVersion: 3, StateVersion: 4}, "two": {ID: "two", UUID: "synthetic-two", Enabled: true, CredentialVersion: 3, StateVersion: 4}}, commits: map[string]Receipt{}}
}
func (f *fakeAccounts) Snapshot(_ context.Context, id string) (Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts[id], nil
}
func (f *fakeAccounts) Counts(context.Context) (map[string]int, error) { return map[string]int{}, nil }
func (f *fakeAccounts) Apply(_ context.Context, w Work) (Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.unknown {
		return Receipt{}, fail("TIMEOUT")
	}
	a := f.accounts[w.Account.ID]
	if a.StateVersion != w.Account.StateVersion {
		return Receipt{}, fail("VERSION_CONFLICT")
	}
	a.URL = w.URL
	a.StateVersion++
	f.accounts[a.ID] = a
	r := Receipt{AccountID: a.UUID, CredentialVersion: a.CredentialVersion, StateVersion: a.StateVersion}
	f.commits[w.OperationID] = r
	if f.lost {
		return r, fail("TIMEOUT")
	}
	return r, nil
}
func (f *fakeAccounts) Committed(_ context.Context, id string) (Receipt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.commits[id]
	return r, ok, nil
}
func targets() []Target {
	return []Target{{ID: "one", CredentialVersion: 3, StateVersion: 4}, {ID: "two", CredentialVersion: 3, StateVersion: 4}}
}
func TestAssignmentStableIdempotencyAndDisabledPreserved(t *testing.T) {
	backend := fakeBackend()
	s, _ := fixture(t, backend)
	v := source(t, s)
	refresh(t, s, v.ID)
	healthy(t, s)
	in := JobInput{Kind: "assign", Mode: "random", Targets: targets()}
	job, err := s.Submit(context.Background(), "fixed-idempotency", in)
	if err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job.ID)
	if result.Done != 2 || backend.calls != 2 {
		t.Fatal(result)
	}
	for _, item := range result.Items {
		if item.Status != "succeeded" || item.Receipt == nil {
			t.Fatal(item)
		}
	}
	a, _ := backend.Snapshot(context.Background(), "one")
	if a.Enabled || a.CredentialVersion != 3 || a.StateVersion != 5 {
		t.Fatal("unrelated account settings changed")
	}
	replay, err := s.Submit(context.Background(), "fixed-idempotency", in)
	if err != nil || replay.ID != job.ID || backend.calls != 2 {
		t.Fatal("request replayed")
	}
	in.SourceID = "different"
	if _, err = s.Submit(context.Background(), "fixed-idempotency", in); Code(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(err)
	}
	public, _ := json.Marshal(result)
	if strings.Contains(string(public), "password") || strings.Contains(string(public), "synthetic-user") || strings.Contains(string(public), "socks5://") {
		t.Fatal("job query leaked auth")
	}
}
func TestNoCandidateArchiveVersionAndUnknownNeverReplay(t *testing.T) {
	backend := fakeBackend()
	s, _ := fixture(t, backend)
	v := source(t, s)
	refresh(t, s, v.ID)
	job, err := s.Submit(context.Background(), newID(), JobInput{Kind: "assign", Mode: "random", Targets: targets()})
	if err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job.ID)
	if result.Items[0].Code != "NO_ELIGIBLE_PROXY" || backend.calls != 0 {
		t.Fatal(result)
	}
	healthy(t, s)
	backend.accounts["two"] = Account{ID: "two", UUID: "synthetic-two", Archived: true, CredentialVersion: 3, StateVersion: 4}
	backend.unknown = true
	in := JobInput{Kind: "assign", Mode: "random", Targets: targets()}
	job, err = s.Submit(context.Background(), "unknown-selection", in)
	if err != nil {
		t.Fatal(err)
	}
	result = waitJob(t, s, job.ID)
	if result.Items[0].Status != "needs_reconcile" || result.Items[1].Status != "skipped" {
		t.Fatal(result)
	}
	work, _, _ := s.itemInput(context.Background(), job.ID, 0)
	replay, _ := s.Submit(context.Background(), "unknown-selection", in)
	again, _, _ := s.itemInput(context.Background(), replay.ID, 0)
	if work.URL != again.URL || work.OperationID != again.OperationID || backend.calls != 1 {
		t.Fatal("unknown request redrawn/replayed")
	}
}
func TestCancelConcurrencyAndRestartNoReplay(t *testing.T) {
	s, _ := fixture(t, nil)
	v := source(t, s)
	refresh(t, s, v.ID)
	entered := make(chan struct{}, 1)
	s.probe = func(ctx context.Context, _ string) ProbeResult {
		entered <- struct{}{}
		<-ctx.Done()
		return ProbeResult{State: "failed", Error: "CANCELLED"}
	}
	job, err := s.Submit(context.Background(), newID(), JobInput{Kind: "test", All: true, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err = s.Submit(context.Background(), newID(), JobInput{Kind: "test", All: true}); Code(err) != "TEST_JOB_ALREADY_RUNNING" {
		t.Fatal(err)
	}
	if err = s.Cancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job.ID)
	if result.Status != "cancelled" || result.Done != 2 {
		t.Fatal(result)
	}
	backend := fakeBackend()
	s.accounts = backend
	input, _ := json.Marshal(Work{Account: backend.accounts["one"], OperationID: "original-op"})
	item, _ := json.Marshal(Item{Index: 0, Status: "running"})
	_, err = s.db.Exec("INSERT INTO jobs(id,kind,status,created_at,concurrency,fingerprint) VALUES('crash','assign','running',?,1,'x')", stamp())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("INSERT INTO job_items VALUES('crash',0,?,?,'running')", input, item)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, _ := s.Job(context.Background(), "crash", 0, 50)
	if recovered.Items[0].Status != "interrupted" || backend.calls != 0 {
		t.Fatal(recovered)
	}
}
func TestNetworkProbeAndPrivateAddressRejection(t *testing.T) {
	for _, address := range []string{"http://example.test/token", "https://127.0.0.1/token", "https://[::1]/token", "https://user:password@example.test/token"} {
		if err := validateDownloadURL(address); err == nil {
			t.Fatal("unsafe download accepted")
		}
	}
	if conn, err := publicDial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		conn.Close()
		t.Fatal("private socket allowed")
	}
	studio := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie on probe")
		}
		if r.URL.Path == "/ip" {
			_, _ = w.Write([]byte(`{"ip":"8.8.8.8"}`))
			return
		}
		w.WriteHeader(studio)
	}))
	defer server.Close()
	result := probeWithClient(context.Background(), server.Client(), server.URL+"/ip", server.URL+"/studio")
	if result.State != "passed" || result.ExitIP != "8.8.8.8" {
		t.Fatal(result)
	}
	studio = 403
	result = probeWithClient(context.Background(), server.Client(), server.URL+"/ip", server.URL+"/studio")
	if result.State != "restricted" {
		t.Fatal(result)
	}
	proxyURL, _ := url.Parse("http://fixture:synthetic-password@proxy.invalid:8080")
	tr := transport(proxyURL)
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
	}
	client := &http.Client{Transport: tr}
	defer client.CloseIdleConnections()
	result = probeWithClient(context.Background(), client, "http://ip.invalid/ip", "http://studio.invalid/studio")
	if result.State != "restricted" {
		t.Fatal(result)
	}
}

func TestProbeConcurrencyBoundAndCredentialChangeNeedsRetest(t *testing.T) {
	s, data := fixture(t, nil)
	source := source(t, s)
	var lines strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&lines, "proxy-%d.invalid:1080:fixture:synthetic-password\n", i)
	}
	*data = []byte(lines.String())
	refresh(t, s, source.ID)
	var mu sync.Mutex
	active, peak := 0, 0
	s.probe = func(context.Context, string) ProbeResult {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(3 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return ProbeResult{State: "passed", ExitIP: "8.8.8.8"}
	}
	job, err := s.Submit(context.Background(), newID(), JobInput{Kind: "test", All: true, Concurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job.ID)
	if peak < 2 || peak > 3 || result.Done != 20 {
		t.Fatal("concurrency bound violated", peak, result.Done)
	}
	*data = []byte("proxy-0.invalid:1080:fixture:different-synthetic-password\n")
	refresh(t, s, source.ID)
	eligible, _ := s.List(context.Background(), Query{Eligible: true})
	if eligible.Total != 0 {
		t.Fatal("new auth inherited old health")
	}
}

func TestReopenRecoversCommittedAssignmentWithoutReplay(t *testing.T) {
	ctx := context.Background()
	backend := fakeBackend()
	path := filepath.Join(t.TempDir(), "reopen.db")
	s, err := Open(ctx, Options{Path: path, Accounts: backend})
	if err != nil {
		t.Fatal(err)
	}
	work := Work{Account: backend.accounts["one"], OperationID: "original-operation", URL: "socks5://fixture:synthetic-password@proxy.invalid:1080"}
	backend.accounts["one"] = Account{ID: "one", UUID: work.Account.UUID, CredentialVersion: 3, StateVersion: 5, URL: work.URL}
	backend.commits[work.OperationID] = Receipt{AccountID: work.Account.UUID, CredentialVersion: 3, StateVersion: 5}
	input, _ := json.Marshal(work)
	result, _ := json.Marshal(Item{Index: 0, Status: "running", AccountID: "one"})
	if _, err = s.db.Exec("INSERT INTO jobs(id,kind,status,created_at,concurrency,fingerprint) VALUES('original','assign','running',?,1,'x')", stamp()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO job_items VALUES('original',0,?,?,'running')", input, result); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, Options{Path: path, Accounts: backend})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.Job(ctx, "original", 0, 50)
	if err != nil || job.Items[0].Status != "succeeded" || backend.calls != 0 {
		t.Fatal("reopen replayed or lost original commit", job, err)
	}
	item := job.Items[0]
	item.Status = "needs_reconcile"
	item.Code = "COMMIT_RESULT_UNKNOWN"
	_ = s.saveItem(job.ID, item)
	backend.accounts["one"] = Account{ID: "one", UUID: work.Account.UUID, CredentialVersion: 3, StateVersion: 6, URL: work.URL}
	job, err = s.Reconcile(ctx, job.ID)
	if err != nil || job.Items[0].Status != "needs_reconcile" || backend.calls != 0 {
		t.Fatal("readback invented success or replayed", job, err)
	}
}

func TestCancellationBeforeWorkerStartsConvergesWithoutProbe(t *testing.T) {
	s, _ := fixture(t, nil)
	s.probe = func(context.Context, string) ProbeResult {
		t.Error("cancelled work probed")
		return ProbeResult{State: "failed"}
	}
	input, _ := json.Marshal(Work{ProxyID: "synthetic"})
	result, _ := json.Marshal(Item{Status: "queued"})
	_, err := s.db.Exec("INSERT INTO jobs(id,kind,status,created_at,concurrency,fingerprint) VALUES('cancel-before-start','test','queued',?,1,'x')", stamp())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("INSERT INTO job_items VALUES('cancel-before-start',0,?,?,'queued')", input, result)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.wg.Add(1)
	s.run(ctx, "cancel-before-start", "test", 1, func() {})
	job, err := s.Job(context.Background(), "cancel-before-start", 0, 50)
	if err != nil || job.Status != "cancelled" || job.Done != 1 || job.Items[0].Code != "CANCELLED" {
		t.Fatal(job, err)
	}
}
