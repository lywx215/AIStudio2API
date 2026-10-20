// Package management implements the versioned management HTTP boundary and its
// process-scoped durable task worker. Native runtime and storage stay behind
// managementcontract interfaces.
package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type httpService struct{ options c.HTTPOptions }

var _ c.HTTPRegistrar = (*httpService)(nil)

func NewHTTP(o c.HTTPOptions) (c.HTTPRegistrar, error) {
	if o.Store == nil || o.Tasks == nil || o.Accounts == nil || o.Projection == nil {
		return nil, invalid()
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.NewID == nil {
		o.NewID = c.NewID
	}
	return &httpService{options: o}, nil
}

type route struct {
	operation, id, allow string
	scope                c.Scope
}

func managementRoute(p, method string) (route, bool) {
	type entry struct {
		path, method, op string
		scope            c.Scope
	}
	entries := []entry{
		{"/system/status", "GET", "status", c.ScopeSystemRead}, {"/system/readiness", "GET", "readiness", c.ScopeSystemRead}, {"/system/capabilities", "GET", "capabilities", c.ScopeSystemRead}, {"/system/diagnostics", "GET", "diagnostics", c.ScopeSystemRead},
		{"/system/reload-auth", "POST", "reload-auth", c.ScopeAccountsWrite},
		{"/accounts", "GET", "accounts", c.ScopeAccountsRead}, {"/accounts/import", "POST", "import", c.ScopeAccountsWrite}, {"/accounts/batch", "POST", "batch", c.ScopeAccountsWrite}, {"/accounts/export", "POST", "export", c.ScopeAccountsExport},
		{"/tasks", "GET", "tasks", c.ScopeTasksRead}, {"/settings", "GET", "settings", c.ScopeSettingsRead}, {"/settings", "PATCH", "settings-patch", c.ScopeSettingsWrite}, {"/usage", "GET", "usage", c.ScopeUsageRead}, {"/audit", "GET", "audit", c.ScopeAuditRead},
	}
	rel := strings.TrimPrefix(p, c.Namespace)
	result := route{}
	for _, e := range entries {
		if rel == e.path {
			if result.allow != "" {
				result.allow += ", "
			}
			result.allow += e.method
			if method == e.method {
				result.operation = e.op
				result.scope = e.scope
			}
		}
	}
	if result.allow != "" {
		return result, true
	}
	parts := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "accounts" || parts[0] == "tasks") {
		result.id = parts[1]
		if parts[0] == "tasks" {
			if len(parts) == 2 {
				result.allow = "GET"
				result.operation = "task"
				result.scope = c.ScopeTasksRead
			} else if len(parts) == 3 && parts[2] == "cancel" {
				result.allow = "POST"
				result.operation = "cancel"
				result.scope = c.ScopeTasksWrite
			}
		} else if len(parts) == 2 {
			result.allow = "GET, PATCH"
			if method == "GET" {
				result.operation = "account"
				result.scope = c.ScopeAccountsRead
			} else if method == "PATCH" {
				result.operation = "account-patch"
				result.scope = c.ScopeAccountsWrite
			}
		} else if len(parts) == 3 {
			switch parts[2] {
			case "credentials":
				result.allow = "PUT"
				result.operation = "replace"
				result.scope = c.ScopeAccountsWrite
			case "test":
				result.allow = "POST"
				result.operation = "test"
				result.scope = c.ScopeAccountsTest
			case "archive", "restore":
				result.allow = "POST"
				result.operation = parts[2]
				result.scope = c.ScopeAccountsArchive
			case "reload":
				result.allow = "POST"
				result.operation = "reload"
				result.scope = c.ScopeAccountsWrite
			}
		}
		if result.allow != "" {
			if !strings.Contains(", "+result.allow+", ", ", "+method+", ") {
				result.operation = ""
			}
			return result, true
		}
	}
	return route{}, false
}

func canonicalPath(r *http.Request, namespace string) bool {
	p := r.URL.Path
	return (p == namespace || strings.HasPrefix(p, namespace+"/")) && path.Clean(p) == p && !strings.Contains(p, "\\") && r.URL.EscapedPath() == p
}

func (h *httpService) requestID() string {
	id, err := h.options.NewID("req")
	if err == nil && c.ValidRequestID(id) {
		return id
	}
	// Even a failing injected entropy source cannot leak an inbound request ID.
	id, _ = c.NewID("req")
	return id
}

func writeResponse(w http.ResponseWriter, id string, status int, data any, err error) {
	var body []byte
	if err != nil {
		public := c.PublicError(err)
		status = public.HTTPStatus()
		body, _ = json.Marshal(c.ErrorEnvelope{Error: public, RequestID: id})
	} else {
		body, err = json.Marshal(c.Envelope[any]{Data: data, RequestID: id})
		if err != nil {
			public := c.NewError(c.CodeInternalError)
			status = public.HTTPStatus()
			body, _ = json.Marshal(c.ErrorEnvelope{Error: public, RequestID: id})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-Id", id)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (h *httpService) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := h.requestID()
		if !canonicalPath(r, c.Namespace) {
			writeResponse(w, id, 0, nil, c.NewError(c.CodeNotFound))
			return
		}
		route, known := managementRoute(r.URL.Path, r.Method)
		if !known {
			writeResponse(w, id, 0, nil, c.NewError(c.CodeNotFound))
			return
		}
		if route.operation == "" {
			w.Header().Set("Allow", route.allow)
			writeResponse(w, id, 0, nil, c.NewError(c.CodeMethodNotAllowed))
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			writeResponse(w, id, 0, nil, c.NewError(c.CodeUnauthorized))
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "mgmt_") {
			writeResponse(w, id, 0, nil, c.NewError(c.CodeUnauthorized))
			return
		}
		principal, err := h.options.Store.AuthenticateKey(r.Context(), parts[1])
		if err != nil {
			writeResponse(w, id, 0, nil, err)
			return
		}
		if !principal.Has(route.scope) {
			writeResponse(w, id, 0, nil, c.NewError(c.CodeForbidden))
			return
		}
		if route.id != "" {
			valid := c.ValidAccountID(route.id)
			if route.operation == "task" || route.operation == "cancel" {
				valid = strings.HasPrefix(route.id, "task_") && c.ValidAccountID(strings.TrimPrefix(route.id, "task_"))
			}
			if !valid {
				writeResponse(w, id, 0, nil, invalid())
				return
			}
		}
		data, status, err := h.dispatch(w, r, route, principal, id)
		writeResponse(w, id, status, data, err)
	})
}

func (h *httpService) dispatch(w http.ResponseWriter, r *http.Request, rt route, p c.Principal, requestID string) (any, int, error) {
	ctx := r.Context()
	o := h.options
	if r.Method == "GET" {
		switch rt.operation {
		case "status":
			v, e := o.Projection.Status(ctx)
			return v, 200, e
		case "readiness":
			v, e := o.Projection.Readiness(ctx)
			if v.Checks == nil {
				v.Checks = []c.Check{}
			}
			return v, 200, e
		case "capabilities":
			v, e := o.Projection.Capabilities(ctx)
			if v.VerificationChannels == nil {
				v.VerificationChannels = []c.Channel{}
			}
			if v.DefaultVerificationChannels == nil {
				v.DefaultVerificationChannels = []c.Channel{}
			}
			return v, 200, e
		case "settings":
			v, e := o.Projection.Settings(ctx)
			if v.PersistentKeys == nil {
				v.PersistentKeys = []string{}
			}
			if v.Values.UpstreamChannels == nil {
				v.Values.UpstreamChannels = []string{}
			}
			return v, 200, e
		case "account":
			v, e := o.Store.GetAccount(ctx, rt.id)
			return v.Account, 200, e
		case "task":
			v, e := o.Store.GetTask(ctx, rt.id)
			return publicTask(v.Task), 200, e
		}
		q, e := pageQuery(r)
		if e != nil {
			return nil, 0, e
		}
		switch rt.operation {
		case "accounts":
			v, e := o.Store.ListAccounts(ctx, q)
			return normalizePage(v), 200, e
		case "tasks":
			v, e := o.Store.ListTasks(ctx, q)
			for i := range v.Items {
				v.Items[i] = publicTask(v.Items[i])
			}
			return normalizePage(v), 200, e
		case "audit":
			v, e := o.Store.ListAudit(ctx, q)
			for i := range v.Items {
				if v.Items[i].Error != nil {
					v.Items[i].Error = c.PublicError(v.Items[i].Error)
				}
			}
			return normalizePage(v), 200, e
		case "usage":
			v, e := o.Projection.Usage(ctx, q)
			return normalizePage(v), 200, e
		case "diagnostics":
			v, e := o.Projection.Diagnostics(ctx, q)
			v.Accounts = normalizePage(v.Accounts)
			return v, 200, e
		}
	}
	var raw []byte
	var err error
	if rt.operation == "replace" {
		raw, err = readBody(w, r, false)
		if err == nil && (len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '"') {
			_, err = object(raw)
		}
	} else {
		raw, err = readObject(w, r, true)
	}
	if err != nil {
		return nil, 0, err
	}
	in := taskInput{RequestID: requestID, Model: c.DefaultModel, Verify: false}
	items := []c.TaskItem{{AccountID: rt.id, Status: c.TaskQueued, Stage: "queued"}}
	kind := c.TaskKind(rt.operation)
	switch rt.operation {
	case "account-patch":
		var body c.AccountPatch
		if required(raw, "enabled") != nil || decode(raw, &body) != nil || body.Enabled == nil || body.ExpectedVersions.Validate(false) != nil {
			return nil, 0, invalid()
		}
		commit, e := o.Accounts.Patch(ctx, c.StateCommand{Operation: c.OperationContext{RequestID: requestID, CreatedByKeyID: p.KeyID}, AccountID: rt.id, Expected: body.ExpectedVersions, Force: body.Force, Enabled: *body.Enabled})
		if e == nil && !commit.Published {
			e = c.NewError(c.CodePersistenceError)
		}
		if e == nil {
			e = h.audit(ctx, p, requestID, "account-patch", rt.id)
		}
		return commit.Account.Account, 200, e
	case "settings-patch":
		var body c.SettingsPatch
		obj, e := object(raw)
		if e != nil || len(obj) == 0 || decode(raw, &body) != nil {
			return nil, 0, invalid()
		}
		for _, v := range []*int{body.WarmWorkerLimit, body.MaxActiveWorkers, body.WarmStartupConcurrency, body.PerAccountConcurrency} {
			if v != nil && (*v < 0 || uint64(*v) > c.MaxSafeInteger) {
				return nil, 0, invalid()
			}
		}
		if body.WarmStartupConcurrency != nil && *body.WarmStartupConcurrency < 1 || body.PerAccountConcurrency != nil && *body.PerAccountConcurrency < 1 {
			return nil, 0, invalid()
		}
		caps, e := o.Projection.Capabilities(ctx)
		if e != nil {
			return nil, 0, e
		}
		if !caps.SettingsWrite {
			return nil, 0, c.NewError(c.CodeInvalidState)
		}
		v, e := o.Projection.PatchSettings(ctx, body)
		if e == nil {
			e = h.audit(ctx, p, requestID, "settings-patch", "")
		}
		return v, 200, e
	case "export":
		var body c.ExportRequest
		if decode(raw, &body) != nil {
			return nil, 0, invalid()
		}
		if err := accountIDs(body.AccountIDs); err != nil {
			return nil, 0, err
		}
		v, e := o.Accounts.Export(ctx, body.AccountIDs)
		if v.Items == nil {
			v.Items = []c.ExportItem{}
		}
		if e == nil {
			e = h.audit(ctx, p, requestID, "export", "")
		}
		return v, 200, e
	case "cancel":
		if !emptyObject(raw) {
			return nil, 0, invalid()
		}
		v, e := o.Tasks.Cancel(ctx, rt.id, p)
		if e == nil {
			e = h.audit(ctx, p, requestID, "cancel", rt.id)
		}
		return publicTask(v), 200, e
	case "import":
		var body c.ImportRequest
		if decode(raw, &body) != nil || required(raw, "items") != nil || len(body.Items) == 0 {
			return nil, 0, invalid()
		}
		if len(body.Items) > c.MaxBatchItems {
			return nil, 0, c.NewError(c.CodePayloadTooLarge)
		}
		in.Verify = body.Verify == nil || *body.Verify
		in.Channels = body.Channels
		if body.Model != "" {
			in.Model = body.Model
		}
		if suppliedEmpty(raw, "model") || !c.ValidModel(in.Model) {
			return nil, 0, invalid()
		}
		items = make([]c.TaskItem, 0, len(body.Items))
		seen := map[string]bool{}
		for _, item := range body.Items {
			if !clientRef(item.ClientRef) || seen[item.ClientRef] {
				return nil, 0, invalid()
			}
			seen[item.ClientRef] = true
			credential, e := credentials(item.Credentials)
			if e != nil {
				return nil, 0, e
			}
			if _, e = o.Accounts.PrepareCredentials(credential); e != nil {
				return nil, 0, e
			}
			in.Credentials = append(in.Credentials, credential)
			items = append(items, c.TaskItem{ClientRef: item.ClientRef, Status: c.TaskQueued, Stage: "queued"})
		}
		if in.Verify && !p.Has(c.ScopeAccountsTest) {
			return nil, 0, c.NewError(c.CodeForbidden)
		}
	case "replace":
		obj, _ := object(raw)
		credentialRaw := json.RawMessage(raw)
		in.Legacy = true
		in.Verify = true
		kind = c.TaskReplace
		if _, ok := obj["credentials"]; ok {
			var body c.ReplaceRequest
			if decode(raw, &body) != nil || required(raw, "credentials", "expectedCredentialVersion", "expectedStateVersion") != nil || body.ExpectedVersions.Validate(true) != nil {
				return nil, 0, invalid()
			}
			in.Legacy = false
			in.Expected = body.ExpectedVersions
			in.Verify = body.Verify == nil || *body.Verify
			in.Channels = body.Channels
			credentialRaw = body.Credentials
		}
		credential, e := credentials(credentialRaw)
		if e != nil {
			return nil, 0, e
		}
		if credential.AccountName == "" {
			record, readErr := o.Store.GetAccount(ctx, rt.id)
			if readErr != nil {
				return nil, 0, readErr
			}
			credential.AccountName = record.NativeID
		}
		if _, e = o.Accounts.PrepareCredentials(credential); e != nil {
			return nil, 0, e
		}
		in.Credentials = []c.Credentials{credential}
		if in.Verify && !p.Has(c.ScopeAccountsTest) {
			return nil, 0, c.NewError(c.CodeForbidden)
		}
	case "test":
		var body c.TestRequest
		if decode(raw, &body) != nil {
			return nil, 0, invalid()
		}
		if body.Model != "" {
			in.Model = body.Model
		}
		if suppliedEmpty(raw, "model") || !c.ValidModel(in.Model) {
			return nil, 0, invalid()
		}
		if body.Mode == "" {
			body.Mode = c.VerificationModel
		}
		if suppliedEmpty(raw, "mode") || (body.Mode != c.VerificationModel && body.Mode != c.VerificationConnection) {
			return nil, 0, invalid()
		}
		in.Action = string(body.Mode)
		in.Channels = body.Channels
	case "batch":
		var body c.BatchRequest
		if decode(raw, &body) != nil {
			return nil, 0, invalid()
		}
		if e := accountIDs(body.AccountIDs); e != nil {
			return nil, 0, e
		}
		if body.Action != "enable" && body.Action != "disable" && body.Action != "archive" {
			return nil, 0, invalid()
		}
		if body.Action == "archive" && !p.Has(c.ScopeAccountsArchive) {
			return nil, 0, c.NewError(c.CodeForbidden)
		}
		in.Action = body.Action
		in.Force = body.Force
		items = make([]c.TaskItem, 0, len(body.AccountIDs))
		for _, id := range body.AccountIDs {
			items = append(items, c.TaskItem{AccountID: id, Status: c.TaskQueued, Stage: "queued"})
		}
	case "archive", "reload":
		var body c.ForceRequest
		if decode(raw, &body) != nil {
			return nil, 0, invalid()
		}
		in.Force = body.Force
	case "restore", "reload-auth":
		if !emptyObject(raw) {
			return nil, 0, invalid()
		}
	default:
		return nil, 0, c.NewError(c.CodeNotFound)
	}
	if rt.operation == "replace" {
		kind = c.TaskReplace
	}
	if kind == c.TaskImport || kind == c.TaskReplace || kind == c.TaskTest {
		obj, _ := object(raw)
		if value, supplied := obj["channels"]; supplied && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, 0, invalid()
		}
		channels, err := c.NormalizeVerificationChannels(in.Channels)
		if err != nil {
			return nil, 0, err
		}
		in.Channels = channels
	}
	accepted, e := h.submit(ctx, r, p, kind, items, in)
	return accepted, http.StatusAccepted, e
}

func emptyObject(raw []byte) bool { obj, e := object(raw); return e == nil && len(obj) == 0 }
func suppliedEmpty(raw []byte, name string) bool {
	obj, _ := object(raw)
	v, ok := obj[name]
	return ok && bytes.Equal(bytes.TrimSpace(v), []byte(`""`))
}

func (h *httpService) submit(ctx context.Context, r *http.Request, p c.Principal, kind c.TaskKind, items []c.TaskItem, in taskInput) (c.TaskAccepted, error) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) == 0 || strings.TrimSpace(keys[0]) == "" {
		return c.TaskAccepted{}, c.NewError(c.CodeIdempotencyKeyRequired)
	}
	if len(keys) != 1 || len(keys[0]) > 256 || keys[0] != strings.TrimSpace(keys[0]) {
		return c.TaskAccepted{}, invalid()
	}
	private, err := json.Marshal(in)
	if err != nil {
		return c.TaskAccepted{}, invalid()
	}
	requestID := in.RequestID
	in.RequestID = ""
	normalized, err := json.Marshal(struct {
		Input taskInput    `json:"input"`
		Items []c.TaskItem `json:"items"`
		Kind  c.TaskKind   `json:"kind"`
	}{in, items, kind})
	if err != nil {
		return c.TaskAccepted{}, invalid()
	}
	// Marshal through a generic object to give nested object keys a stable order.
	var value any
	d := json.NewDecoder(bytes.NewReader(normalized))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return c.TaskAccepted{}, invalid()
	}
	normalized, _ = json.Marshal(value)
	digest := sha256.Sum256(append([]byte(r.Method+"\n"+r.URL.Path+"\n"), normalized...))
	return h.options.Tasks.Submit(ctx, c.Admission{Principal: p, RequestID: requestID, IdempotencyKey: keys[0], CanonicalDigest: digest, Kind: kind, Items: items, PrivateInput: private, Capture: c.TaskCaptureInstruction{Mode: captureMode(kind), Expected: in.Expected}})
}

func (h *httpService) audit(_ context.Context, p c.Principal, req, action, account string) error {
	ctx, cancel := cleanupContext()
	defer cancel()
	id, e := h.options.NewID("audit")
	if e != nil {
		return c.NewError(c.CodeAuditPersistenceFailed)
	}
	entry := c.AuditEntry{AuditID: id, RequestID: req, CreatedByKeyID: p.KeyID, Action: action, AccountID: account, Timestamp: c.NewTimestamp(h.options.Clock()), Outcome: "succeeded"}
	if action == "cancel" {
		entry.TaskID = account
		entry.AccountID = ""
	}
	e = h.options.Store.AppendAudit(ctx, entry)
	if e != nil {
		return c.NewError(c.CodeAuditPersistenceFailed)
	}
	return nil
}

func publicTask(task c.Task) c.Task {
	task.Items = append([]c.TaskItem{}, task.Items...)
	if task.Error != nil {
		task.Error = c.PublicError(task.Error)
	}
	for i := range task.Items {
		if task.Items[i].Error != nil {
			task.Items[i].Error = c.PublicError(task.Items[i].Error)
		}
	}
	return task
}

func (h *httpService) KeyHandler(authorizer c.ConsoleAuthorizer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := h.requestID()
		fail := func(e error) { writeResponse(w, id, 0, nil, e) }
		if !canonicalPath(r, c.KeyNamespace) {
			fail(c.NewError(c.CodeNotFound))
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, c.KeyNamespace)
		allowed := "GET, POST"
		keyID := ""
		if rel != "" {
			if !strings.HasPrefix(rel, "/") || strings.Contains(rel[1:], "/") {
				fail(c.NewError(c.CodeNotFound))
				return
			}
			keyID = rel[1:]
			allowed = "DELETE"
		}
		if !strings.Contains(", "+allowed+", ", ", "+r.Method+", ") {
			w.Header().Set("Allow", allowed)
			fail(c.NewError(c.CodeMethodNotAllowed))
			return
		}
		if authorizer == nil {
			fail(c.NewError(c.CodeConsolePasswordRequired))
			return
		}
		if err := authorizer.AuthorizeConsole(r); err != nil {
			fail(c.NewError(c.CodeConsolePasswordRequired))
			return
		}
		if !consoleOrigin(r) {
			fail(c.NewError(c.CodeForbidden))
			return
		}
		if r.Method != "GET" && r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			fail(c.NewError(c.CodeForbidden))
			return
		}
		switch r.Method {
		case "GET":
			v, e := h.options.Store.ListKeys(r.Context())
			if v.Items == nil {
				v.Items = []c.ManagementKey{}
			}
			for i := range v.Items {
				if v.Items[i].Scopes == nil {
					v.Items[i].Scopes = []c.Scope{}
				}
			}
			writeResponse(w, id, 200, v, e)
		case "POST":
			raw, e := readObject(w, r, false)
			if e != nil {
				fail(e)
				return
			}
			var body c.KeyCreateRequest
			if decode(raw, &body) != nil || strings.TrimSpace(body.Name) == "" || len(body.Name) > 256 || len(body.Scopes) == 0 || len(body.Scopes) > len(c.Scopes()) {
				fail(invalid())
				return
			}
			valid := map[c.Scope]bool{}
			for _, scope := range c.Scopes() {
				valid[scope] = true
			}
			for _, scope := range body.Scopes {
				if !valid[scope] {
					fail(invalid())
					return
				}
				valid[scope] = false
			}
			if body.ExpiresAt != nil && !body.ExpiresAt.After(h.options.Clock()) {
				fail(invalid())
				return
			}
			v, e := h.options.Store.CreateKey(r.Context(), body)
			writeResponse(w, id, 200, v, e)
		case "DELETE":
			if !strings.HasPrefix(keyID, "mkey_") || !c.ValidAccountID(strings.TrimPrefix(keyID, "mkey_")) {
				fail(invalid())
				return
			}
			raw, e := readObject(w, r, true)
			if e != nil {
				fail(e)
				return
			}
			if !emptyObject(raw) {
				fail(invalid())
				return
			}
			v, e := h.options.Store.RevokeKey(r.Context(), keyID)
			if e == nil {
				clean, cancel := cleanupContext()
				e = h.options.Tasks.CancelForKey(clean, keyID)
				cancel()
				if e != nil {
					e = c.NewError(c.CodePersistenceError)
				}
			}
			writeResponse(w, id, 200, v, e)
		}
	})
}

func consoleOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site != "" && site != "same-origin" && site != "none" {
		return false
	}
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	u, err := url.Parse(values[0])
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return values[0] == scheme+"://"+r.Host
}
