package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
	"github.com/Mag1cFall/AIStudio2API/internal/config"
	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
	"github.com/Mag1cFall/AIStudio2API/internal/managementverify"
)

// G0 supplies a snapshot of the selected account's effective network/WAA
// configuration. This resolver must not return a production worker or auth path.
type managementVerificationEnvironment struct {
	WAABackend   string
	CamoufoxPath string
	Proxy        string
	Locale       string
	Timezone     string
	InitTimeout  time.Duration
}

type managementVerificationSnapshotter interface {
	SnapshotCredentials(context.Context, string) (c.AccountRecord, c.CredentialMaterial, error)
}

type managementVerificationTargetOptions struct {
	Accounts    managementVerificationSnapshotter
	Environment func(context.Context, string) (managementVerificationEnvironment, error)
	// Empty uses the operating system's temporary directory. No auth directory
	// or production runtime directory is ever used for these files.
	TempDir string
	NewID   c.IDGenerator
}

type managementVerificationWorker interface {
	aistudio.ProtectedPreparer
	aistudio.ProtocolHeaderProvider
	Close() error
}

type managementVerificationTarget struct {
	options  managementVerificationTargetOptions
	worker   func(context.Context, string, camoufoxnative.Options) (managementVerificationWorker, error)
	protocol func(*aistudio.AccountPool, managementVerificationWorker, managementVerificationEnvironment) (aistudio.RPCTransport, aistudio.ProtectedTransport, func(), error)
	identity func(context.Context, aistudio.StorageState, managementVerificationEnvironment) (string, int, error)
}

var _ c.VerificationTarget = (*managementVerificationTarget)(nil)

func newManagementVerificationTarget(options managementVerificationTargetOptions) (c.VerificationTarget, error) {
	if options.Accounts == nil || options.Environment == nil {
		return nil, fmt.Errorf("management verification requires account snapshots and an environment resolver")
	}
	if options.NewID == nil {
		options.NewID = c.NewID
	}
	return &managementVerificationTarget{
		options: options,
		worker: func(ctx context.Context, id string, options camoufoxnative.Options) (managementVerificationWorker, error) {
			return newWAAWorker(ctx, id, options)
		},
		protocol: newManagementVerificationProtocol,
		identity: managementVerificationIdentity,
	}, nil
}

func (t *managementVerificationTarget) Open(ctx context.Context, input c.VerificationInput) (_ c.VerificationSession, err error) {
	channels, channelErr := c.NormalizeVerificationChannels(input.Channels)
	if channelErr != nil || len(channels) != 1 {
		return nil, c.NewError(c.CodeInvalidRequest)
	}
	input.Channels = channels
	channel := aistudio.Channel(channels[0])
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.ValidAccountID(input.Account.AccountID) || !c.ValidModel(input.Model) || !managementVerificationNativeID(input.Account.NativeID) {
		return nil, c.NewError(c.CodeInvalidRequest)
	}
	record, material, err := t.options.Accounts.SnapshotCredentials(ctx, input.Account.AccountID)
	if err != nil {
		return nil, err
	}
	if record.AccountID != input.Account.AccountID || record.NativeID != input.Account.NativeID || record.Index != input.Account.Index || record.Versions != input.Account.Versions || record.CredentialDigest != input.Account.CredentialDigest {
		return nil, c.NewError(c.CodeVersionConflict)
	}
	if record.Deleted || record.Archived {
		return nil, c.NewError(c.CodeInvalidState)
	}
	if input.Candidate != nil {
		material = *input.Candidate
	}
	if material.NativeID != record.NativeID || len(material.StorageState) == 0 || len(material.StorageState) > c.MaxCredentialBytes {
		return nil, c.NewError(c.CodeInvalidCredentials)
	}
	var state aistudio.StorageState
	if json.Unmarshal(material.StorageState, &state) != nil || state.Validate() != nil {
		return nil, c.NewError(c.CodeInvalidCredentials)
	}
	if _, err := aistudio.NewSigner().Authorization(state); err != nil {
		return nil, c.NewError(c.CodeInvalidCredentials)
	}
	env, err := t.options.Environment(ctx, record.NativeID)
	if err != nil {
		return nil, err
	}
	switch env.WAABackend {
	case config.WAABackendGo:
		env.CamoufoxPath = ""
	case config.WAABackendCamoufox:
		if strings.TrimSpace(env.CamoufoxPath) == "" {
			return nil, c.NewError(c.CodeInvalidState)
		}
	default:
		return nil, c.NewError(c.CodeInvalidState)
	}
	if env.InitTimeout <= 0 || env.InitTimeout > c.VerificationTimeout {
		env.InitTimeout = 2 * time.Minute
	}
	root, err := os.MkdirTemp(t.options.TempDir, "aistudio-management-verify-")
	if err != nil {
		return nil, c.NewError(c.CodeInternalError)
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &managementVerificationSession{input: input, env: env, root: root, cancel: cancel, context: ctx, initial: append([]byte(nil), material.StorageState...), identity: t.identity}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	directory := filepath.Join(root, "accounts", record.NativeID)
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	accountConfig := aistudio.DefaultAccountConfig(record.NativeID)
	accountConfig.Proxy, accountConfig.Locale, accountConfig.Timezone = env.Proxy, env.Locale, env.Timezone
	// This enabled flag belongs only to the temporary copy; production flags are
	// never written, enabled or cleared by verification.
	accountConfig.Enabled = true
	configJSON, err := json.Marshal(accountConfig)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(directory, "account.json")
	storagePath := filepath.Join(directory, "storage-state.json")
	if err = os.WriteFile(configPath, configJSON, 0o600); err != nil {
		return nil, err
	}
	if err = os.WriteFile(storagePath, material.StorageState, 0o600); err != nil {
		return nil, err
	}
	account := &aistudio.Account{ID: record.NativeID, Directory: directory, ConfigPath: configPath, StoragePath: storagePath, RuntimePath: filepath.Join(directory, "runtime.json"), Config: accountConfig, StorageState: state, State: aistudio.AccountReady}
	s.pool = aistudio.NewAccountPool([]*aistudio.Account{account}, 1)
	s.pool.SetUpstreamChannels([]aistudio.Channel{channel})
	s.lease, err = s.pool.AcquireAccountChannel(ctx, record.NativeID, channel)
	if err != nil {
		return nil, err
	}
	initCtx, initCancel := context.WithTimeout(ctx, env.InitTimeout)
	s.worker, err = t.worker(initCtx, record.NativeID, camoufoxnative.Options{
		ExecutablePath: env.CamoufoxPath, StorageStatePath: storagePath, Model: input.Model,
		BootstrapPrompt: managementverify.ProbePrompt, Locale: env.Locale, Timezone: env.Timezone,
		Proxy: env.Proxy, Headless: true, TemporaryChat: true, ReadyTimeout: env.InitTimeout,
	})
	initCancel()
	if err != nil {
		return nil, err
	}
	if s.worker == nil {
		return nil, c.NewError(c.CodeVerificationFailed)
	}
	rpc, protected, closeProtocol, err := t.protocol(s.pool, s.worker, env)
	if err != nil {
		return nil, err
	}
	s.closeProtocol = closeProtocol
	s.guard = &managementVerificationTransport{accountID: record.NativeID, model: input.Model, channel: channels[0], next: protected}
	s.catalog = &managementVerificationRPC{accountID: record.NativeID, channel: channels[0], next: rpc}
	s.client, err = aistudio.NewClient(aistudio.ClientOptions{Transport: s.catalog, Protected: s.guard, PinnedChannel: channel})
	if err != nil {
		return nil, err
	}
	s.requestID, err = t.options.NewID("req")
	if err != nil || !c.ValidRequestID(s.requestID) {
		return nil, c.NewError(c.CodeInternalError)
	}
	return s, nil
}

func managementVerificationNativeID(id string) bool {
	parsed, err := mail.ParseAddress(id)
	return err == nil && parsed.Address == id && id == strings.ToLower(strings.TrimSpace(id)) && filepath.Base(id) == id && !strings.ContainsAny(id, `/\<>:"|?*`)
}

func newManagementVerificationProtocol(pool *aistudio.AccountPool, worker managementVerificationWorker, env managementVerificationEnvironment) (aistudio.RPCTransport, aistudio.ProtectedTransport, func(), error) {
	rpc, err := aistudio.NewMakerSuiteHTTPTransport(aistudio.HTTPTransportOptions{Pool: pool, Signer: aistudio.NewSigner(), Headers: worker, GlobalProxy: env.Proxy})
	if err != nil {
		return nil, nil, nil, err
	}
	protected, err := aistudio.NewWorkerProtectedTransport(aistudio.WorkerProtectedTransportOptions{
		Transport: rpc, SetupTimeout: env.InitTimeout,
		Workers: aistudio.ProtectedPreparerProviderFunc(func(ctx context.Context, id, model string) (aistudio.ProtectedPreparer, error) {
			lease, ok := aistudio.AccountLeaseFromContext(ctx)
			if !ok || lease.Account().ID != id || (lease.Channel() == aistudio.ChannelBuild && !pool.BuildEnabled()) || (lease.Channel() == aistudio.ChannelPlayground && !pool.PlaygroundEnabled()) {
				return nil, c.NewError(c.CodeVerificationFailed)
			}
			return worker, nil
		}),
	})
	if err != nil {
		rpc.CloseIdleConnections()
		return nil, nil, nil, err
	}
	return rpc, protected, rpc.CloseIdleConnections, nil
}

type managementVerificationSession struct {
	input         c.VerificationInput
	env           managementVerificationEnvironment
	root          string
	context       context.Context
	cancel        context.CancelFunc
	initial       []byte
	pool          *aistudio.AccountPool
	lease         *aistudio.AccountLease
	worker        managementVerificationWorker
	client        *aistudio.Client
	guard         *managementVerificationTransport
	catalog       *managementVerificationRPC
	closeProtocol func()
	identity      func(context.Context, aistudio.StorageState, managementVerificationEnvironment) (string, int, error)
	requestID     string
	runMu         sync.Mutex
	ran           bool
	closed        bool
	refreshed     *c.CredentialMaterial
	closeOnce     sync.Once
	closeErr      error
}

var _ c.VerificationSession = (*managementVerificationSession)(nil)
var _ managementverify.CandidateSource = (*managementVerificationSession)(nil)

func (s *managementVerificationSession) Run(ctx context.Context, probe c.VerificationProbe, progress c.ProgressFunc) (obs c.VerificationObservation, err error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.ran || s.closed {
		return obs, c.NewError(c.CodeInvalidState)
	}
	s.ran = true
	if probe.Channel == "" {
		probe.Channel = s.input.Channels[0]
	}
	if probe.Channel != s.input.Channels[0] || probe.Model != s.input.Model || probe.Mode != s.input.Mode || probe.Prompt != managementverify.ProbePrompt || probe.MaxOutputTokens != managementverify.ProbeMaxOutputTokens {
		return obs, c.NewError(c.CodeInvalidRequest)
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.context, cancel)
	defer stop()
	defer cancel()
	if s.context.Err() != nil {
		return obs, s.context.Err()
	}
	ctx = aistudio.ContextWithAccountLease(ctx, s.lease)
	state, err := s.worker.BrowserStorageState(ctx)
	if err != nil {
		return obs, err
	}
	identity, _, err := s.identity(ctx, state, s.env)
	if err != nil || identity != s.input.Account.NativeID {
		return obs, c.NewError(c.CodeVerificationFailed)
	}
	obs = c.VerificationObservation{AccountID: s.input.Account.AccountID, NativeID: identity, Model: s.input.Model, RequestID: s.requestID, IdentityConfirmed: true}
	defer func() {
		if probe.Mode == c.VerificationConnection {
			obs.ActualChannel, obs.UpstreamStatus = s.catalog.evidence()
		} else {
			obs.ActualChannel, obs.UpstreamStatus = s.guard.evidence()
		}
	}()
	var models []aistudio.Model
	if probe.Channel == c.ChannelBuild {
		models, err = s.client.BuildModelsForAccount(ctx, identity)
	} else {
		models, err = s.client.ModelsForAccount(ctx, identity)
	}
	if err != nil {
		return obs, err
	}
	// WorkerProtectedTransport validates the selected model against the lease's
	// pool. Install the catalogue only in this temporary account, never in the
	// production pool. Using the protocol client's cache alone is insufficient.
	if probe.Channel == c.ChannelBuild {
		err = s.pool.SetBuildCatalog(identity, models)
	} else {
		err = s.pool.SetCatalog(identity, s.lease.Account().BenefitTier, models)
	}
	if err != nil {
		return obs, err
	}
	var selected *aistudio.Model
	for i := range models {
		if models[i].ID == probe.Model {
			selected = &models[i]
			break
		}
	}
	if selected == nil || !slices.Contains(selected.Methods, "generateContent") || selected.Capabilities["assistant_route"] || selected.Capabilities["interactions_api"] || selected.Capabilities["interaction_route"] {
		return obs, c.NewError(c.CodeVerificationFailed)
	}
	if probe.Mode == c.VerificationConnection {
		// ListModels and reliable identity prove only connectivity to Playground.
		obs.ActualChannel, obs.UpstreamStatus = s.catalog.evidence()
		if err := s.captureCandidate(ctx); err != nil {
			return obs, err
		}
		return obs, nil
	}
	if progress != nil {
		progress("generating", 45)
	}
	maxTokens := int64(probe.MaxOutputTokens)
	request := aistudio.GenerateRequest{ID: s.requestID, Model: probe.Model, AccountID: identity,
		Contents: []aistudio.Content{{Role: aistudio.RoleUser, Parts: []aistudio.Part{{Text: probe.Prompt}}}},
		Config:   aistudio.GenerationConfig{MaxOutputTokens: &maxTokens},
	}
	// Client is the single-account protocol core, never PooledService/ordinary
	// routing. The transport below admits exactly one GenerateContent RPC and
	// rejects every Build/assistant/interaction/continuation path before sending.
	events, err := s.client.Generate(ctx, request)
	if err != nil {
		return obs, err
	}
	defer func() {
		cancel()
		for range events {
		}
	}()
	var visible strings.Builder
	for {
		select {
		case <-ctx.Done():
			return obs, ctx.Err()
		case event, ok := <-events:
			if !ok {
				obs.VisibleText = visible.String()
				obs.ActualChannel, obs.UpstreamStatus = s.guard.evidence()
				if ctx.Err() != nil {
					return obs, ctx.Err()
				}
				if !obs.Finished {
					return obs, c.NewError(c.CodeVerificationFailed)
				}
				if err := s.captureCandidate(ctx); err != nil {
					return obs, err
				}
				return obs, nil
			}
			if event.ProviderModel != probe.Model {
				return obs, c.NewError(c.CodeVerificationFailed)
			}
			switch event.Kind {
			case aistudio.EventText:
				if visible.Len()+len(event.Text) > 64<<10 {
					return obs, c.NewError(c.CodeVerificationFailed)
				}
				visible.WriteString(event.Text)
			case aistudio.EventFinish:
				if event.FinishReason != "stop" && event.FinishReason != "max_tokens" {
					return obs, c.NewError(c.CodeVerificationFailed)
				}
				obs.Finished = true
			case aistudio.EventError:
				return obs, c.NewError(c.CodeVerificationFailed)
			}
		}
	}
}

func (s *managementVerificationSession) captureCandidate(ctx context.Context) error {
	if s.input.Candidate == nil {
		return nil
	}
	state, err := s.worker.BrowserStorageState(ctx)
	if err != nil {
		return err
	}
	var original aistudio.StorageState
	if err := json.Unmarshal(s.initial, &original); err != nil {
		return err
	}
	before, err := json.Marshal(original)
	if err != nil {
		return err
	}
	original.Cookies = state.Cookies
	if _, err := aistudio.NewSigner().Authorization(original); err != nil {
		return c.NewError(c.CodeVerificationFailed)
	}
	encoded, err := json.Marshal(original)
	if err != nil || len(encoded) > c.MaxCredentialBytes {
		return c.NewError(c.CodeVerificationFailed)
	}
	// Preserve native extensions and origins; only private candidate cookies may
	// be replaced. No snapshot, lease or production state is published here.
	if sha256.Sum256(encoded) != sha256.Sum256(before) {
		s.refreshed = &c.CredentialMaterial{StorageState: encoded, NativeID: s.input.Account.NativeID}
	}
	return nil
}

func (s *managementVerificationSession) RefreshedCandidate(ctx context.Context) (*c.CredentialMaterial, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed {
		return nil, c.NewError(c.CodeInvalidState)
	}
	if s.refreshed == nil {
		return nil, nil
	}
	material := *s.refreshed
	material.StorageState = append([]byte(nil), material.StorageState...)
	return &material, nil
}

func (s *managementVerificationSession) Close() error {
	s.cancel()
	s.closeOnce.Do(func() {
		s.runMu.Lock()
		defer s.runMu.Unlock()
		s.closed = true
		if s.worker != nil {
			s.closeErr = errors.Join(s.closeErr, s.worker.Close())
		}
		if s.closeProtocol != nil {
			s.closeProtocol()
		}
		if s.lease != nil {
			s.closeErr = errors.Join(s.closeErr, s.lease.Release())
		}
		// root is the exact directory created by MkdirTemp, never an auth path.
		s.closeErr = errors.Join(s.closeErr, os.RemoveAll(s.root))
		s.initial, s.refreshed = nil, nil
	})
	return s.closeErr
}

// This gate records the actual protected RPC branch. Neither the configured
// channel nor the HTTP 200 by itself can set Playground generation evidence.
type managementVerificationTransport struct {
	accountID string
	model     string
	next      aistudio.ProtectedTransport
	mu        sync.Mutex
	sent      bool
	channel   c.Channel
	observed  c.Channel
	status    int
}

func (t *managementVerificationTransport) DoProtected(ctx context.Context, request aistudio.GenerateRequest, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
	t.mu.Lock()
	if t.sent || t.next == nil || request.AccountID != t.accountID || request.Model != t.model || rpc.AccountID != t.accountID || rpc.RequestID != request.ID || !c.ValidRequestID(request.ID) || !t.validRPC(request, rpc) {
		t.mu.Unlock()
		return nil, c.NewError(c.CodeVerificationFailed)
	}
	t.sent = true
	t.mu.Unlock()
	response, err := t.next.DoProtected(ctx, request, rpc)
	if response != nil {
		t.mu.Lock()
		t.observed, t.status = t.selectedChannel(), response.StatusCode
		t.mu.Unlock()
	}
	return response, err
}

func (t *managementVerificationTransport) selectedChannel() c.Channel {
	if t.channel == "" {
		return c.ChannelPlayground
	}
	return t.channel
}
func (t *managementVerificationTransport) validRPC(request aistudio.GenerateRequest, rpc aistudio.RPCRequest) bool {
	if t.selectedChannel() == c.ChannelBuild {
		if rpc.Method != "ProxyStreamedCall" && rpc.Method != "ProxyUnaryCall" {
			return false
		}
		if rpc.URL != aistudio.MakerSuiteRPCBase+rpc.Method || rpc.Streaming != (rpc.Method == "ProxyStreamedCall") {
			return false
		}
		var envelope []json.RawMessage
		var path, raw string
		if json.Unmarshal(rpc.Body, &envelope) != nil || len(envelope) < 3 || len(envelope) > 4 || json.Unmarshal(envelope[0], &path) != nil || json.Unmarshal(envelope[1], &raw) != nil {
			return false
		}
		suffix := ":streamGenerateContent"
		if rpc.Method == "ProxyUnaryCall" {
			suffix = ":generateContent"
			var method string
			if len(envelope) != 4 || json.Unmarshal(envelope[3], &method) != nil || method != "POST" {
				return false
			}
		} else if len(envelope) != 3 {
			return false
		}
		var body struct {
			GenerationConfig struct {
				MaxOutputTokens int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		return path == "/v1beta/models/"+t.model+suffix && json.Unmarshal([]byte(raw), &body) == nil && body.GenerationConfig.MaxOutputTokens > 0 && body.GenerationConfig.MaxOutputTokens <= managementverify.ProbeMaxOutputTokens
	}
	if rpc.Method != "GenerateContent" || rpc.URL != aistudio.MakerSuiteRPCBase+"GenerateContent" || !rpc.Streaming {
		return false
	}
	var body []json.RawMessage
	var model string
	var config []json.RawMessage
	var maxOutput int64
	return json.Unmarshal(rpc.Body, &body) == nil && len(body) >= 5 && json.Unmarshal(body[0], &model) == nil && model == "models/"+t.model && json.Unmarshal(body[3], &config) == nil && len(config) >= 4 && json.Unmarshal(config[3], &maxOutput) == nil && maxOutput > 0 && maxOutput <= managementverify.ProbeMaxOutputTokens
}

func (t *managementVerificationTransport) evidence() (c.Channel, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observed, t.status
}

type managementVerificationRPC struct {
	channel   c.Channel
	accountID string
	next      aistudio.RPCTransport
	mu        sync.Mutex
	status    int
}

func (t *managementVerificationRPC) Do(ctx context.Context, rpc aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
	if t.next == nil || rpc.AccountID != t.accountID || !t.validRPC(rpc) {
		return nil, c.NewError(c.CodeVerificationFailed)
	}
	response, err := t.next.Do(ctx, rpc)
	if response != nil {
		t.mu.Lock()
		t.status = response.StatusCode
		t.mu.Unlock()
	}
	return response, err
}

func (t *managementVerificationRPC) evidence() (c.Channel, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status == 0 {
		return "", 0
	}
	if t.channel == c.ChannelBuild {
		return c.ChannelBuild, t.status
	}
	return c.ChannelPlayground, t.status
}

func (t *managementVerificationRPC) validRPC(rpc aistudio.RPCRequest) bool {
	if t.channel != c.ChannelBuild {
		return rpc.Method == "ListModels" && rpc.URL == aistudio.MakerSuiteRPCBase+"ListModels"
	}
	if rpc.Method != "ProxyUnaryCall" || rpc.URL != aistudio.MakerSuiteRPCBase+"ProxyUnaryCall" || rpc.Streaming {
		return false
	}
	var body []json.RawMessage
	var path, method string
	return json.Unmarshal(rpc.Body, &body) == nil && len(body) == 4 && json.Unmarshal(body[0], &path) == nil && path == "/v1beta/models" && json.Unmarshal(body[3], &method) == nil && method == "GET"
}
