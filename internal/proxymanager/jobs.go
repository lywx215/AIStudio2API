package proxymanager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (s *Service) AccountContexts(ctx context.Context, ids []string) ([]Account, error) {
	if s.accounts == nil {
		return nil, fail("ACCOUNT_SERVICE_UNAVAILABLE")
	}
	if len(ids) == 0 || len(ids) > 200 {
		return nil, fail("INVALID_SELECTION")
	}
	result := []Account{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || id == "" {
			return nil, fail("INVALID_SELECTION")
		}
		seen[id] = true
		a, err := s.accounts.Snapshot(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, nil
}
func (s *Service) privateProxies(ctx context.Context, q Query, ids []string, all bool) ([]Work, error) {
	w, args, err := where(q)
	if err != nil {
		return nil, err
	}
	if !all {
		if len(ids) == 0 || len(ids) > 20000 {
			return nil, fail("INVALID_SELECTION")
		}
		seen := map[string]bool{}
		marks := []string{}
		for _, id := range ids {
			if id == "" || seen[id] {
				return nil, fail("INVALID_SELECTION")
			}
			seen[id] = true
			marks = append(marks, "?")
			args = append(args, id)
		}
		w += " AND p.id IN (" + strings.Join(marks, ",") + ")"
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+proxyColumns+" FROM proxies p WHERE "+w+" ORDER BY p.endpoint,p.id LIMIT 20001", args...)
	if err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	defer rows.Close()
	works := []Work{}
	for rows.Next() {
		v, raw, err := scanProxy(rows)
		if err != nil {
			return nil, fail("STORAGE_FAILED")
		}
		if !q.Eligible && !v.Enabled {
			continue
		}
		works = append(works, Work{ProxyID: v.ID, ProxyRevision: v.Revision, URL: raw})
	}
	if len(works) > 20000 {
		return nil, fail("SELECTION_TOO_LARGE")
	}
	if rows.Err() != nil {
		return nil, fail("STORAGE_FAILED")
	}
	return works, nil
}
func (s *Service) Submit(ctx context.Context, key string, in JobInput) (Job, error) {
	if len(key) < 8 || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
		return Job{}, fail("INVALID_IDEMPOTENCY_KEY")
	}
	if in.Kind != "test" && in.Kind != "assign" {
		return Job{}, fail("INVALID_JOB_KIND")
	}
	if in.Concurrency == 0 {
		in.Concurrency = 10
	}
	if in.Concurrency < 1 || in.Concurrency > 50 {
		return Job{}, fail("INVALID_CONCURRENCY")
	}
	if in.Kind == "assign" && (len(in.Targets) == 0 || len(in.Targets) > 200 || (in.Mode != "random" && in.Mode != "specified") || in.Mode == "specified" && in.ProxyID == "") {
		return Job{}, fail("INVALID_SELECTION")
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(digest[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Job{}, fail("SERVICE_STOPPED")
	}
	var existing, oldFingerprint string
	err := s.db.QueryRowContext(ctx, "SELECT id,fingerprint FROM jobs WHERE idempotency_key=?", key).Scan(&existing, &oldFingerprint)
	if err == nil {
		if oldFingerprint != fingerprint {
			return Job{}, fail("IDEMPOTENCY_CONFLICT")
		}
		return s.Job(ctx, existing, 0, 50)
	}
	if err != sql.ErrNoRows {
		return Job{}, fail("STORAGE_FAILED")
	}
	err = nil
	if in.Kind == "test" {
		var active int
		if s.db.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE kind='test' AND status IN ('queued','running')").Scan(&active) != nil {
			return Job{}, fail("STORAGE_FAILED")
		}
		if active > 0 {
			return Job{}, fail("TEST_JOB_ALREADY_RUNNING")
		}
	}
	var works []Work
	initial := []Item{}
	if in.Kind == "test" {
		works, err = s.privateProxies(ctx, in.Query, in.IDs, in.All)
	} else {
		if s.accounts == nil {
			return Job{}, fail("ACCOUNT_SERVICE_UNAVAILABLE")
		}
		pool, loadErr := s.privateProxies(ctx, Query{SourceID: in.SourceID, Eligible: true}, nil, true)
		if loadErr != nil {
			return Job{}, loadErr
		}
		seen := map[string]bool{}
		for _, target := range in.Targets {
			if target.ID == "" || seen[target.ID] || target.CredentialVersion == 0 || target.StateVersion == 0 {
				return Job{}, fail("INVALID_SELECTION")
			}
			seen[target.ID] = true
			a, snapshotErr := s.accounts.Snapshot(ctx, target.ID)
			work := Work{Account: a}
			work.Account.ID = target.ID
			item := Item{Index: len(works), AccountID: target.ID, Status: "queued"}
			switch {
			case snapshotErr != nil:
				item.Status = "failed"
				item.Code = Code(snapshotErr)
			case a.Archived || a.Deleted:
				item.Status = "skipped"
				item.Code = "ACCOUNT_ARCHIVED_OR_DELETED"
			case a.CredentialVersion != target.CredentialVersion || a.StateVersion != target.StateVersion:
				item.Status = "failed"
				item.Code = "VERSION_CONFLICT"
			default:
				candidates := []Work{}
				for _, candidate := range pool {
					if candidate.URL != a.URL && (in.Mode == "random" || candidate.ProxyID == in.ProxyID) {
						candidates = append(candidates, candidate)
					}
				}
				if len(candidates) == 0 {
					item.Status = "failed"
					item.Code = "NO_ELIGIBLE_PROXY"
				} else {
					choice, randomErr := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
					if randomErr != nil {
						return Job{}, fail("RANDOM_FAILED")
					}
					selected := candidates[choice.Int64()]
					work.ProxyID, work.ProxyRevision, work.URL = selected.ProxyID, selected.ProxyRevision, selected.URL
					work.OperationID, err = c.NewID("op")
					if err != nil {
						return Job{}, fail("RANDOM_FAILED")
					}
					u, _ := url.Parse(work.URL)
					item.ProxyID, item.Endpoint = work.ProxyID, u.Host
				}
			}
			works = append(works, work)
			initial = append(initial, item)
		}
	}
	if err != nil {
		return Job{}, err
	}
	if len(works) == 0 {
		return Job{}, fail("EMPTY_SELECTION")
	}
	id := newID()
	created := stamp()
	err = transaction(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO jobs(id,kind,status,created_at,concurrency,idempotency_key,fingerprint) VALUES(?,?,'queued',?,?,?,?)", id, in.Kind, created, in.Concurrency, key, fingerprint); err != nil {
			return err
		}
		for index, work := range works {
			input, _ := json.Marshal(work)
			item := Item{Index: index, ProxyID: work.ProxyID, Status: "queued"}
			if len(initial) > index {
				item = initial[index]
			} else {
				u, _ := url.Parse(work.URL)
				item.Endpoint = u.Host
			}
			result, _ := json.Marshal(item)
			if _, err := tx.ExecContext(ctx, "INSERT INTO job_items(job_id,idx,input,result,status) VALUES(?,?,?,?,?)", id, index, input, result, item.Status); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Job{}, safeStoreError(err)
	}
	life, cancel := context.WithCancel(s.ctx)
	s.running[id] = cancel
	s.wg.Add(1)
	go s.run(life, id, in.Kind, in.Concurrency, cancel)
	return s.Job(ctx, id, 0, 50)
}
func (s *Service) Job(ctx context.Context, id string, offset, limit int) (Job, error) {
	if limit == 0 {
		limit = 50
	}
	if offset < 0 || limit < 1 || limit > 200 {
		return Job{}, fail("INVALID_QUERY")
	}
	v := Job{ID: id, Items: []Item{}, Offset: offset, Limit: limit}
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		if tx.QueryRowContext(ctx, "SELECT kind,status,created_at,cancelled FROM jobs WHERE id=?", id).Scan(&v.Kind, &v.Status, &v.CreatedAt, &v.CancelRequested) != nil {
			return fail("JOB_NOT_FOUND")
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(status NOT IN ('queued','running')),0) FROM job_items WHERE job_id=?", id).Scan(&v.Total, &v.Done); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT result FROM job_items WHERE job_id=? ORDER BY idx LIMIT ? OFFSET ?", id, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			var item Item
			if rows.Scan(&data) != nil || json.Unmarshal(data, &item) != nil {
				return fail("STORAGE_FAILED")
			}
			v.Items = append(v.Items, item)
		}
		return rows.Err()
	})
	return v, safeNullable(err)
}
func (s *Service) Jobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM jobs ORDER BY created_at DESC,id LIMIT 20")
	if err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, fail("STORAGE_FAILED")
		}
		ids = append(ids, id)
	}
	rows.Close()
	result := []Job{}
	for _, id := range ids {
		v, err := s.Job(ctx, id, 0, 1)
		if err != nil {
			return nil, err
		}
		v.Items = []Item{}
		result = append(result, v)
	}
	return result, nil
}

func (s *Service) FindJob(ctx context.Context, key string) (Job, error) {
	var id string
	if s.db.QueryRowContext(ctx, "SELECT id FROM jobs WHERE idempotency_key=?", key).Scan(&id) != nil {
		return Job{}, fail("JOB_NOT_FOUND")
	}
	return s.Job(ctx, id, 0, 50)
}
func (s *Service) Cancel(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.db.ExecContext(ctx, "UPDATE jobs SET cancelled=1 WHERE id=? AND status IN ('queued','running')", id)
	if err != nil {
		return fail("STORAGE_FAILED")
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fail("JOB_NOT_RUNNING")
	}
	if cancel := s.running[id]; cancel != nil {
		cancel()
	}
	return nil
}
func (s *Service) saveItem(id string, item Item) error {
	data, _ := json.Marshal(item)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, "UPDATE job_items SET status=?,result=? WHERE job_id=? AND idx=?", item.Status, data, id, item.Index)
	return safeNullable(err)
}
func (s *Service) itemInput(ctx context.Context, id string, index int) (Work, Item, error) {
	var raw, result []byte
	var w Work
	var item Item
	if s.db.QueryRowContext(ctx, "SELECT input,result FROM job_items WHERE job_id=? AND idx=?", id, index).Scan(&raw, &result) != nil || json.Unmarshal(raw, &w) != nil || json.Unmarshal(result, &item) != nil {
		return w, item, fail("STORAGE_FAILED")
	}
	return w, item, nil
}
func (s *Service) run(ctx context.Context, id, kind string, concurrency int, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	defer func() { s.mu.Lock(); delete(s.running, id); s.mu.Unlock() }()
	_, _ = s.db.ExecContext(context.Background(), "UPDATE jobs SET status='running' WHERE id=?", id)
	var total int
	if s.db.QueryRowContext(context.Background(), "SELECT count(*) FROM job_items WHERE job_id=?", id).Scan(&total) != nil {
		_, _ = s.db.ExecContext(context.Background(), "UPDATE jobs SET status='interrupted' WHERE id=?", id)
		return
	}
	queue := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range queue {
				work, item, err := s.itemInput(context.Background(), id, index)
				if err != nil {
					continue
				}
				if item.Status != "queued" {
					continue
				}
				if ctx.Err() != nil {
					item.Status = "skipped"
					item.Code = "CANCELLED"
					_ = s.saveItem(id, item)
					continue
				}
				item.Status = "running"
				if s.saveItem(id, item) != nil {
					continue
				}
				if kind == "test" {
					s.testItem(ctx, id, work, item)
				} else {
					s.assignItem(ctx, id, work, item)
				}
			}
		}()
	}
feed:
	for i := 0; i < total; i++ {
		select {
		case <-ctx.Done():
			break feed
		case queue <- i:
		}
	}
	close(queue)
	workers.Wait()
	status := "completed"
	if ctx.Err() != nil {
		status = "cancelled"
		_, _ = s.db.ExecContext(context.Background(), "UPDATE job_items SET status='skipped',result=json_set(CAST(result AS TEXT),'$.status','skipped','$.code','CANCELLED') WHERE job_id=? AND status='queued'", id)
	}
	_, _ = s.db.ExecContext(context.Background(), "UPDATE jobs SET status=? WHERE id=?", status, id)
}
func (s *Service) testItem(ctx context.Context, id string, work Work, item Item) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	result := s.probe(probeCtx, work.URL)
	cancel()
	if result.State != "passed" && result.State != "failed" && result.State != "restricted" {
		result = ProbeResult{State: "failed", Error: "INVALID_PROBE_RESULT"}
	}
	item.Status = "succeeded"
	item.Probe = &result
	item.Code = result.Error
	if result.State != "passed" {
		item.Status = "failed"
	}
	if ctx.Err() != nil {
		item.Status = "skipped"
		item.Code = "CANCELLED"
		_ = s.saveItem(id, item)
		return
	}
	r, err := s.db.ExecContext(ctx, "UPDATE proxies SET state=?,exit_ip=?,latency=?,studio_latency=?,checked_at=?,error=? WHERE id=? AND revision=?", result.State, result.ExitIP, result.LatencyMS, result.StudioLatencyMS, stamp(), result.Error, work.ProxyID, work.ProxyRevision)
	if err != nil {
		item.Status = "needs_reconcile"
		item.Code = "STORAGE_FAILED"
	} else if n, _ := r.RowsAffected(); n != 1 {
		item.Status = "skipped"
		item.Code = "PROXY_CHANGED"
	}
	_ = s.saveItem(id, item)
}
func (s *Service) eligible(ctx context.Context, work Work) bool {
	var n int
	return s.db.QueryRowContext(ctx, "SELECT count(*) FROM proxies p WHERE p.id=? AND p.revision=? AND p.url=? AND p.enabled=1 AND p.state='passed' AND p.checked_at>=? AND "+presentSQL, work.ProxyID, work.ProxyRevision, work.URL, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano)).Scan(&n) == nil && n == 1
}
func (s *Service) assignItem(ctx context.Context, id string, work Work, item Item) {
	if !s.eligible(ctx, work) {
		item.Status = "failed"
		item.Code = "PROXY_NO_LONGER_ELIGIBLE"
		_ = s.saveItem(id, item)
		return
	}
	operation, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err := s.accounts.Apply(operation, work)
	cancel()
	// Always use the original journal, even when Apply returned a timeout after
	// durable commit. Readback never retries the account mutation.
	check, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	receipt, committed, readErr := s.accounts.Committed(check, work.OperationID)
	if readErr == nil && committed {
		current, snapshotErr := s.accounts.Snapshot(check, work.Account.ID)
		if snapshotErr == nil && current.UUID == receipt.AccountID && current.CredentialVersion == receipt.CredentialVersion && current.StateVersion == receipt.StateVersion && current.URL == work.URL {
			item.Status = "succeeded"
			item.Code = "PROXY_APPLIED"
			item.Receipt = &receipt
		} else {
			item.Status = "needs_reconcile"
			item.Code = "ACCOUNT_CHANGED_AFTER_COMMIT"
		}
		if err != nil {
			item.Status = "needs_reconcile"
			item.Code = "RUNTIME_APPLY_UNCONFIRMED"
		}
	}
	if !committed || readErr != nil {
		if err != nil && readErr == nil && (Code(err) == "VERSION_CONFLICT" || Code(err) == "ACCOUNT_ARCHIVED_OR_DELETED" || Code(err) == "ACCOUNT_BUSY") {
			item.Status = "failed"
			item.Code = Code(err)
		} else {
			item.Status = "needs_reconcile"
			item.Code = "COMMIT_RESULT_UNKNOWN"
		}
	}
	_ = s.saveItem(id, item)
}
func (s *Service) recover(ctx context.Context) error {
	return s.review(ctx, "", true)
}

// Reconcile reads original journals only, without proxy selection or mutation.
func (s *Service) Reconcile(ctx context.Context, id string) (Job, error) {
	job, err := s.Job(ctx, id, 0, 1)
	if err != nil {
		return job, err
	}
	if job.Status == "running" || job.Status == "queued" {
		return job, fail("JOB_STILL_RUNNING")
	}
	if err = s.review(ctx, id, false); err != nil {
		return job, err
	}
	return s.Job(ctx, id, 0, 50)
}
func (s *Service) review(ctx context.Context, id string, restart bool) error {
	rows, err := s.db.QueryContext(ctx, "SELECT j.id,j.kind,i.idx FROM jobs j JOIN job_items i ON i.job_id=j.id WHERE i.status IN ('queued','running','needs_reconcile','interrupted') AND (?='' OR j.id=?) ORDER BY j.created_at,i.idx", id, id)
	if err != nil {
		return fail("STORAGE_FAILED")
	}
	type pending struct {
		id, kind string
		index    int
	}
	list := []pending{}
	for rows.Next() {
		var v pending
		if rows.Scan(&v.id, &v.kind, &v.index) != nil {
			rows.Close()
			return fail("STORAGE_FAILED")
		}
		list = append(list, v)
	}
	rows.Close()
	for _, v := range list {
		work, item, err := s.itemInput(ctx, v.id, v.index)
		if err != nil {
			return err
		}
		if !restart && item.Code == "RUNTIME_APPLY_UNCONFIRMED" {
			continue
		}
		if item.Status != "needs_reconcile" {
			item.Status = "interrupted"
			item.Code = "PROCESS_RESTARTED"
		}
		if v.kind == "assign" && s.accounts != nil && work.OperationID != "" {
			receipt, committed, err := s.accounts.Committed(ctx, work.OperationID)
			if err != nil {
				item.Status = "needs_reconcile"
				item.Code = "COMMIT_RESULT_UNKNOWN"
			} else if committed {
				current, err := s.accounts.Snapshot(ctx, work.Account.ID)
				item.Status = "needs_reconcile"
				item.Code = "ACCOUNT_CHANGED_AFTER_COMMIT"
				if err == nil && current.UUID == receipt.AccountID && current.URL == work.URL && current.StateVersion == receipt.StateVersion && current.CredentialVersion == receipt.CredentialVersion {
					item.Status = "succeeded"
					item.Code = "PROXY_APPLIED"
					item.Receipt = &receipt
				}
			}
		}
		if err = s.saveItem(v.id, item); err != nil {
			return err
		}
	}
	if restart {
		_, err = s.db.ExecContext(ctx, "UPDATE jobs SET status='interrupted' WHERE status IN ('queued','running')")
	}
	return safeNullable(err)
}
