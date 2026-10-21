package proxymanager

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func normalizeProxy(line, protocol string) (string, string, error) {
	line = strings.TrimSpace(line)
	if len(line) > 8192 {
		return "", "", fail("INVALID_PROXY")
	}
	if !strings.Contains(line, "://") {
		parts := strings.SplitN(line, ":", 4)
		if len(parts) != 2 && len(parts) != 4 {
			return "", "", fail("INVALID_PROXY")
		}
		u := &url.URL{Scheme: protocol, Host: net.JoinHostPort(parts[0], parts[1])}
		if len(parts) == 4 {
			if parts[2] == "" {
				return "", "", fail("INVALID_PROXY")
			}
			u.User = url.UserPassword(parts[2], parts[3])
		}
		line = u.String()
	}
	u, err := url.Parse(line)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return "", "", fail("INVALID_PROXY")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if !validProtocol(u.Scheme) {
		return "", "", fail("INVALID_PROXY")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "socks5" {
			return "", "", fail("INVALID_PROXY")
		}
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", "", fail("INVALID_PROXY")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if strings.ContainsAny(host, "\\/@?#% \t\r\n") {
		return "", "", fail("INVALID_PROXY")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !publicIP(ip) {
			return "", "", fail("PRIVATE_PROXY_ADDRESS")
		}
		host = ip.String()
	} else if !validHostname(host) {
		return "", "", fail("INVALID_PROXY")
	}
	u.Host = net.JoinHostPort(host, strconv.Itoa(n))
	u.Path = ""
	u.RawPath = ""
	if u.User != nil {
		if u.User.Username() == "" {
			return "", "", fail("INVALID_PROXY")
		}
		password, _ := u.User.Password()
		u.User = url.UserPassword(u.User.Username(), password)
	}
	return u.String(), u.Host, nil
}
func validProtocol(p string) bool { return p == "socks5" || p == "http" || p == "https" }
func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func Parse(data []byte, protocol string) ([]string, int, error) {
	if len(data) > 5<<20 || !validProtocol(protocol) {
		return nil, 0, fail("LIST_TOO_LARGE_OR_INVALID_PROTOCOL")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > 20001 {
		return nil, 0, fail("LIST_TOO_LARGE")
	}
	seen := map[string]bool{}
	result := []string{}
	invalid := 0
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		value, _, err := normalizeProxy(line, protocol)
		if err != nil {
			invalid++
			continue
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		return nil, invalid, fail("EMPTY_PROXY_LIST")
	}
	return result, invalid, nil
}
func (s *Service) Sources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,kind,protocol,enabled,revision,url<>'',refreshed_at,error FROM sources ORDER BY name,id")
	if err != nil {
		return nil, fail("STORAGE_FAILED")
	}
	defer rows.Close()
	result := []Source{}
	for rows.Next() {
		var v Source
		if rows.Scan(&v.ID, &v.Name, &v.Kind, &v.Protocol, &v.Enabled, &v.Revision, &v.Configured, &v.RefreshedAt, &v.Error) != nil {
			return nil, fail("STORAGE_FAILED")
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Service) SaveSource(ctx context.Context, id string, in SourceInput) (Source, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	if in.Kind == "" {
		in.Kind = "webshare"
	}
	if in.Protocol == "" {
		in.Protocol = "socks5"
	}
	if in.Name == "" || len(in.Name) > 120 || strings.ContainsAny(in.Name, "\r\n") || !validProtocol(in.Protocol) || !hasDownloader(in.Kind) {
		return Source{}, fail("INVALID_SOURCE")
	}
	if in.URL != "" {
		if err := validateDownloadURL(in.URL); err != nil {
			return Source{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := transaction(ctx, s.db, func(tx *sql.Tx) error {
		if id == "" {
			if in.URL == "" {
				return fail("SOURCE_URL_REQUIRED")
			}
			id = newID()
			_, err := tx.ExecContext(ctx, "INSERT INTO sources(id,name,kind,protocol,url,enabled,revision) VALUES(?,?,?,?,?,?,1)", id, in.Name, in.Kind, in.Protocol, in.URL, in.Enabled)
			return err
		}
		var revision int64
		var old string
		if tx.QueryRowContext(ctx, "SELECT revision,url FROM sources WHERE id=?", id).Scan(&revision, &old) != nil {
			return fail("SOURCE_NOT_FOUND")
		}
		if revision != in.Revision {
			return fail("VERSION_CONFLICT")
		}
		if in.URL == "" {
			in.URL = old
		}
		if in.URL == "" {
			return fail("SOURCE_URL_REQUIRED")
		}
		_, err := tx.ExecContext(ctx, "UPDATE sources SET name=?,kind=?,protocol=?,url=?,enabled=?,revision=revision+1 WHERE id=?", in.Name, in.Kind, in.Protocol, in.URL, in.Enabled, id)
		return err
	})
	if err != nil {
		return Source{}, safeStoreError(err)
	}
	all, err := s.Sources(ctx)
	for _, v := range all {
		if v.ID == id {
			return v, err
		}
	}
	return Source{}, fail("SOURCE_NOT_FOUND")
}
func safeStoreError(err error) error {
	if _, ok := err.(*Error); ok {
		return err
	}
	return fail("STORAGE_FAILED")
}
func (s *Service) DeleteSource(ctx context.Context, id string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return transaction(ctx, s.db, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE sources SET enabled=0,url='',revision=revision+1 WHERE id=? AND revision=?", id, revision)
		if err != nil {
			return fail("STORAGE_FAILED")
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return fail("VERSION_CONFLICT")
		}
		_, err = tx.ExecContext(ctx, "UPDATE memberships SET present=0 WHERE source_id=?", id)
		return safeNullable(err)
	})
}
func safeNullable(err error) error {
	if err != nil {
		return safeStoreError(err)
	}
	return nil
}
func (s *Service) Refresh(ctx context.Context, id string) (int, int, error) {
	var address, protocol, kind string
	var revision int64
	if s.db.QueryRowContext(ctx, "SELECT url,protocol,kind,revision FROM sources WHERE id=? AND enabled=1", id).Scan(&address, &protocol, &kind, &revision) != nil || address == "" {
		return 0, 0, fail("SOURCE_NOT_AVAILABLE")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := sourceDownload(ctx, kind, address, s.download)
	var values []string
	invalid := 0
	if err == nil {
		values, invalid, err = Parse(data, protocol)
	}
	if err != nil {
		_, _ = s.db.ExecContext(context.WithoutCancel(ctx), "UPDATE sources SET error=? WHERE id=? AND revision=?", Code(err), id, revision)
		return 0, invalid, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = transaction(ctx, s.db, func(tx *sql.Tx) error {
		var current int64
		if tx.QueryRowContext(ctx, "SELECT revision FROM sources WHERE id=?", id).Scan(&current) != nil || current != revision {
			return fail("VERSION_CONFLICT")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE memberships SET present=0 WHERE source_id=?", id); err != nil {
			return err
		}
		for _, value := range values {
			u, _ := url.Parse(value)
			pid := newID()
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO proxies(id,url,protocol,endpoint) VALUES(?,?,?,?)", pid, value, u.Scheme, u.Host); err != nil {
				return err
			}
			if err := tx.QueryRowContext(ctx, "SELECT id FROM proxies WHERE url=?", value).Scan(&pid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO memberships(source_id,proxy_id,present) VALUES(?,?,1) ON CONFLICT(source_id,proxy_id) DO UPDATE SET present=1", id, pid); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE sources SET refreshed_at=?,error='',revision=revision+1 WHERE id=?", stamp(), id)
		return err
	})
	return len(values), invalid, safeNullable(err)
}

const presentSQL = `EXISTS(SELECT 1 FROM memberships m JOIN sources s ON s.id=m.source_id WHERE m.proxy_id=p.id AND m.present=1 AND s.enabled=1)`

func where(q Query) (string, []any, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 200 || q.Offset < 0 || len(q.Search) > 200 {
		return "", nil, fail("INVALID_QUERY")
	}
	w := []string{"1=1"}
	args := []any{}
	if q.Search != "" {
		w = append(w, "p.endpoint LIKE ? ESCAPE '\\'")
		value := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q.Search)
		args = append(args, "%"+value+"%")
	}
	if q.SourceID != "" {
		w = append(w, "EXISTS(SELECT 1 FROM memberships m WHERE m.proxy_id=p.id AND m.source_id=? AND m.present=1)")
		args = append(args, q.SourceID)
	}
	if q.Protocol != "" {
		if !validProtocol(q.Protocol) {
			return "", nil, fail("INVALID_QUERY")
		}
		w = append(w, "p.protocol=?")
		args = append(args, q.Protocol)
	}
	if q.State != "" {
		switch q.State {
		case "passed", "failed", "restricted", "untested":
			w = append(w, "p.state=?")
			args = append(args, q.State)
		case "removed":
			w = append(w, "NOT "+presentSQL)
		case "disabled":
			w = append(w, "p.enabled=0")
		default:
			return "", nil, fail("INVALID_QUERY")
		}
	}
	if q.Eligible {
		w = append(w, "p.enabled=1 AND p.state='passed' AND p.checked_at>=? AND "+presentSQL)
		args = append(args, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano))
	}
	return strings.Join(w, " AND "), args, nil
}

const proxyColumns = `p.id,p.url,p.protocol,p.endpoint,p.enabled,p.revision,p.state,p.exit_ip,p.latency,p.studio_latency,p.checked_at,p.error,` + presentSQL + `,COALESCE((SELECT group_concat(s.name,', ') FROM memberships m JOIN sources s ON s.id=m.source_id WHERE m.proxy_id=p.id AND m.present=1),'')`

type scanner interface{ Scan(...any) error }

func scanProxy(row scanner) (Proxy, string, error) {
	var v Proxy
	var raw string
	err := row.Scan(&v.ID, &raw, &v.Protocol, &v.Endpoint, &v.Enabled, &v.Revision, &v.State, &v.ExitIP, &v.LatencyMS, &v.StudioLatencyMS, &v.CheckedAt, &v.Error, &v.Present, &v.Sources)
	checked, _ := time.Parse(time.RFC3339Nano, v.CheckedAt)
	v.Eligible = v.Enabled && v.Present && v.State == "passed" && time.Since(checked) <= 24*time.Hour
	return v, raw, err
}
func (s *Service) List(ctx context.Context, q Query) (Page, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	w, args, err := where(q)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: []Proxy{}, Offset: q.Offset, Limit: q.Limit}
	counts := map[string]int{}
	if s.accounts != nil {
		counts, err = s.accounts.Counts(ctx)
		if err != nil {
			return page, err
		}
	}
	err = transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM proxies p WHERE "+w, args...).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+proxyColumns+" FROM proxies p WHERE "+w+" ORDER BY p.endpoint,p.id LIMIT ? OFFSET ?", append(args, q.Limit, q.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, raw, err := scanProxy(rows)
			if err != nil {
				return err
			}
			v.Accounts = counts[raw]
			page.Items = append(page.Items, v)
		}
		return rows.Err()
	})
	return page, safeNullable(err)
}
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var v Stats
	err := s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(state='passed'),0),COALESCE(sum(state='failed'),0),COALESCE(sum(state='restricted'),0),COALESCE(sum(state='untested'),0),COALESCE(sum(enabled=1 AND state='passed' AND checked_at>=? AND `+strings.ReplaceAll(presentSQL, "p.id", "proxies.id")+`),0),COALESCE(avg(CASE WHEN state='passed' AND checked_at>=? THEN latency END),0) FROM proxies`, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano), time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano)).Scan(&v.Total, &v.Passed, &v.Failed, &v.Restricted, &v.Untested, &v.Eligible, &v.AverageMS)
	return v, safeNullable(err)
}
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.db.ExecContext(ctx, "UPDATE proxies SET enabled=?,revision=revision+1 WHERE id=? AND revision=?", enabled, id, revision)
	if err != nil {
		return fail("STORAGE_FAILED")
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return fail("VERSION_CONFLICT")
	}
	return nil
}
