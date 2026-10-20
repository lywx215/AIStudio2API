package managementcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestTimestampUTCAndPrecision(t *testing.T) {
	value := NewTimestamp(time.Date(2026, 10, 20, 12, 34, 56, 123999999, time.FixedZone("UTC+8", 8*3600)))
	data, err := json.Marshal(value)
	if err != nil || string(data) != `"2026-10-20T04:34:56.123Z"` {
		t.Fatalf("time=%s err=%v", data, err)
	}
	for _, bad := range []string{`null`, `"2026-10-20T04:34:56Z"`, `"2026-10-20T04:34:56.123456Z"`, `"2026-10-20T12:34:56.123+08:00"`} {
		if err := json.Unmarshal([]byte(bad), &value); err == nil {
			t.Fatalf("accepted noncanonical timestamp %s", bad)
		}
	}
	if _, err := json.Marshal(Timestamp{}); err == nil {
		t.Fatal("zero timestamp serialized")
	}
}

func TestIDsAndCAS(t *testing.T) {
	seen := map[string]bool{}
	for range 256 {
		id, err := NewID("req")
		if err != nil || !ValidRequestID(id) || seen[id] {
			t.Fatal("invalid or repeated request identity")
		}
		seen[id] = true
	}
	id, err := NewID("")
	if err != nil || !ValidAccountID(id) || !ValidID(id) {
		t.Fatal("invalid account identity")
	}
	if _, err := NewID("arbitrary"); err == nil {
		t.Fatal("arbitrary prefix accepted")
	}
	zero, one := uint64(0), uint64(1)
	for _, v := range []ExpectedVersions{{Credential: &one}, {State: &one}, {Credential: &zero, State: &one}} {
		if v.Validate(false) == nil {
			t.Fatal("partial/zero CAS accepted")
		}
	}
	if (ExpectedVersions{}).Validate(true) == nil || (ExpectedVersions{Credential: &one, State: &one}).Validate(true) != nil {
		t.Fatal("required CAS validation")
	}
	var omitted, disabled AccountPatch
	if json.Unmarshal([]byte(`{}`), &omitted) != nil || json.Unmarshal([]byte(`{"enabled":false}`), &disabled) != nil || omitted.Enabled != nil || disabled.Enabled == nil || *disabled.Enabled {
		t.Fatal("enabled presence lost")
	}
}

func TestErrorProjectionAndScopeIsolation(t *testing.T) {
	for code, spec := range ErrorCatalog() {
		e := NewError(code)
		if e.HTTPStatus() != spec.Status || e.Message != spec.Message {
			t.Fatal(code)
		}
	}
	if got := PublicError(fmt.Errorf("wrapped: %w", &Error{Code: CodeInvalidCredentials, Message: "secret fixture"})); got.Message != "Invalid credential storage state." {
		t.Fatal("unsafe typed message")
	}
	if got := PublicError(errors.New("secret fixture")); got.Code != CodeInternalError || strings.Contains(got.Message, "fixture") {
		t.Fatal("unsafe implementation message")
	}
	copy := ErrorCatalog()
	delete(copy, CodeUnauthorized)
	if NewError(CodeUnauthorized).HTTPStatus() != 401 {
		t.Fatal("mutable error catalog")
	}
	if slices.Contains(ScopeTemplate("operator"), ScopeAuditRead) || !slices.Contains(ScopeTemplate("readonly"), ScopeAuditRead) || len(ScopeTemplate("unknown")) != 0 {
		t.Fatal("scope templates")
	}
	if _, err := json.Marshal(CredentialMaterial{StorageState: json.RawMessage(`{"secret":"fixture"}`)}); err == nil {
		t.Fatal("internal credential material serialized")
	}
}

func TestOpenAPISchemasAndWireDTOs(t *testing.T) {
	raw, err := os.ReadFile("../../docs/management/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const resource = "https://management.invalid/openapi.json"
	if err := compiler.AddResource(resource, document); err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
		Errors map[ErrorCode]struct {
			Status  int    `json:"httpStatus"`
			Message string `json:"message"`
		} `json:"x-error-catalog"`
		Templates map[string][]Scope `json:"x-scope-templates"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	compiled := map[string]*jsonschema.Schema{}
	for name := range spec.Components.Schemas {
		schema, err := compiler.Compile(resource + "#/components/schemas/" + name)
		if err != nil {
			t.Fatalf("schema %s: %v", name, err)
		}
		compiled[name] = schema
	}
	for code, expected := range ErrorCatalog() {
		if actual, ok := spec.Errors[code]; !ok || actual.Status != expected.Status || actual.Message != expected.Message {
			t.Fatalf("catalog drift: %s", code)
		}
	}
	if len(spec.Errors) != len(ErrorCatalog()) {
		t.Fatal("error catalog length drift")
	}
	for _, name := range []string{"readonly", "operator", "admin"} {
		if !reflect.DeepEqual(spec.Templates[name], ScopeTemplate(name)) {
			t.Fatalf("scope drift: %s", name)
		}
	}
	now := NewTimestamp(time.Date(2026, 10, 20, 0, 0, 0, 123000000, time.UTC))
	account := Account{AccountID: "ec5f01c8-1bee-43df-957b-20f857b38bda", Index: 0, AccountState: AccountState{Enabled: true}, Versions: Versions{1, 1}}
	receipt := UploadReceipt{Status: "committed", AccountID: account.AccountID, Index: 0, Versions: account.Versions, CommittedAt: now}
	upload := UploadOnlyResult{Success: true, Stage: "uploaded", Versions: account.Versions}
	status := 200
	verified := VerificationResult{Success: true, AuthIndex: 0, Model: DefaultModel, RequestID: "req_9175b180-cf82-45df-b568-0a21eb1614e4", UpstreamStatus: &status, Stage: "model_verified", Versions: account.Versions, ActualChannel: ChannelPlayground}
	item := TaskItem{ClientRef: "fixture", AccountID: account.AccountID, Index: &account.Index, Status: TaskSucceeded, Progress: 100, Stage: "uploaded", Upload: &receipt, Result: &ItemResult{Upload: &upload}}
	task := Task{TaskID: "task_9175b180-cf82-45df-b568-0a21eb1614e4", Kind: TaskImport, Status: TaskSucceeded, CreatedByKeyID: "mkey_9175b180-cf82-45df-b568-0a21eb1614e4", CreatedAt: now, UpdatedAt: now, FinishedAt: &now, Counts: TaskCounts{Total: 1, Succeeded: 1}, Items: []TaskItem{item}}
	settings := SettingsValues{WAABackend: "go", WarmStartupConcurrency: 1, PerAccountConcurrency: 1, RoutingStrategy: "round_robin", UpstreamChannels: []string{"playground"}}
	cases := map[string]any{
		"Account": account, "UploadReceipt": receipt, "UploadOnlyResult": upload, "PlaygroundVerificationResult": verified,
		"Task": task, "TaskItem": item, "TaskAccepted": TaskAccepted{TaskID: task.TaskID, Status: TaskQueued},
		"AccountPage": Page[Account]{Items: []Account{account}, Total: 1, Limit: 50},
		"KeyList":     KeyList{Items: []ManagementKey{}}, "ManagementKey": ManagementKey{ID: task.CreatedByKeyID, Name: "fixture", Scopes: []Scope{ScopeAccountsRead}, CreatedAt: now},
		"SettingsSnapshot": SettingsSnapshot{Values: settings, PersistentKeys: []string{}},
		"SettingsUpdated":  SettingsUpdated{Values: settings},
		"Diagnostics":      Diagnostics{ServiceState: "STOPPED", WAABackend: "go", Accounts: Page[AccountRuntimeState]{Items: []AccountRuntimeState{}, Limit: 50}},
		"Capabilities":     Capabilities{Backend: "aistudio2api", ProtocolVersion: ProtocolVersion, VerificationChannels: []Channel{ChannelPlayground}},
		"SystemStatus":     SystemStatus{ServiceState: "STOPPED", WAABackend: "go"},
		"Readiness":        Readiness{Checks: []Check{}},
		"UsageRecord":      UsageRecord{RequestID: "native_fixture", Outcome: "success", StartedAt: now, FinishedAt: now},
		"ErrorEnvelope":    ErrorEnvelope{Error: NewError(CodeInvalidRequest), RequestID: verified.RequestID},
	}
	for name, value := range cases {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiled[name].Validate(decoded); err != nil {
			t.Fatalf("%s does not match OpenAPI: %v", name, err)
		}
	}
	data, _ := json.Marshal(verified)
	decoded, _ := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if compiled["VerificationResult"].Validate(decoded) == nil {
		t.Fatal("Go extension silently accepted by original closed schema")
	}
	delete(decoded.(map[string]any), "actualChannel")
	if compiled["PlaygroundVerificationResult"].Validate(decoded) == nil {
		t.Fatal("Playground channel proof is optional")
	}
	for _, bad := range []string{`{}`, `{"enabled":null}`, `{"enabled":true,"expectedCredentialVersion":1}`} {
		value, _ := jsonschema.UnmarshalJSON(strings.NewReader(bad))
		if compiled["AccountPatch"].Validate(value) == nil {
			t.Fatalf("invalid patch accepted: %s", bad)
		}
	}
}
