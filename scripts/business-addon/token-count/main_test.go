package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/addons/business/vault"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func syntheticResponse(status int, data string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(data))}
}

func TestMeasureUploadsExactBytesAndNeverSendsGeneration(t *testing.T) {
	text := []byte("synthetic exact UTF-8 text\n")
	counts := map[string]int{}
	account := vault.Account{ID: "acct-" + strings.Repeat("a", 32), Enabled: true, Epoch: 1, Credential: vault.Credential{SecureCSES: "synthetic-cookie", CSESIndex: "synthetic-index", ConfigID: "synthetic-config"}}
	result := measure(context.Background(), []vault.Account{account}, text, fakeTransport(func(r *http.Request) (*http.Response, error) {
		counts[r.URL.Path]++
		switch r.URL.Path {
		case authPath:
			return syntheticResponse(200, `{"keyId":"synthetic","xsrfToken":"`+base64.RawURLEncoding.EncodeToString([]byte("synthetic-secret"))+`"}`), nil
		case apiPrefix + "widgetCreateSession":
			return syntheticResponse(200, `{"session":{"name":"sessions/synthetic"}}`), nil
		case apiPrefix + "widgetAddContextFile":
			var request struct {
				Add struct {
					Data string `json:"fileContents"`
					MIME string `json:"mimeType"`
					Name string `json:"name"`
				} `json:"addContextFileRequest"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				t.Fatal("invalid synthetic upload")
			}
			data, err := base64.StdEncoding.DecodeString(request.Add.Data)
			if err != nil || !bytes.Equal(data, text) || request.Add.MIME != "text/plain" || request.Add.Name != "sessions/synthetic" {
				t.Fatal("uploaded bytes or session changed")
			}
			return syntheticResponse(200, `{"addContextFileResponse":{"fileId":"private-upstream-file-id","tokenCount":123456,"opaqueSecret":"never-report-this"},"privateValue":"never-report-that"}`), nil
		default:
			t.Fatal("generation or unrelated request reached underlying transport", r.URL.Path)
			return nil, nil
		}
	}))
	if !result.Completed || result.TokenCount == nil || *result.TokenCount != 123456 || result.GenerationRequestSent || !result.GenerationRequestIntercepted || result.GenerationEvents != 0 || len(counts) != 3 {
		t.Fatal(result, counts)
	}
	encoded, _ := json.Marshal(result)
	for _, secret := range []string{"synthetic-cookie", "synthetic-secret", "synthetic-config", "private-upstream-file-id", "never-report-this", "never-report-that"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private response value escaped")
		}
	}
}

func TestCounterResponseOnlyKnownPositiveIntegerAndStructuralKeys(t *testing.T) {
	for _, raw := range []string{`123456`, `"123456"`} {
		result := report{}
		inspectCounterResponse([]byte(`{"addContextFileResponse":{"tokenCount":`+raw+`}}`), &result)
		if result.TokenCount == nil || *result.TokenCount != 123456 {
			t.Fatal(result)
		}
	}
	for _, raw := range []string{
		`{"tokenCount":999}`, `{"other":{"tokenCount":999}}`,
		`{"addContextFileResponse":{"tokenCount":1.5}}`, `{"addContextFileResponse":{"tokenCount":1e5}}`,
		`{"addContextFileResponse":{"tokenCount":-1}}`, `{"addContextFileResponse":{"tokenCount":0}}`,
		`{"addContextFileResponse":{"tokenCount":null}}`, `{"addContextFileResponse":{"tokenCount":true}}`,
		`{"addContextFileResponse":{"tokenCount":1,"tokenCount":2}}`,
		`{"addContextFileResponse":{"tokenCount":"18446744073709551616"}}`,
	} {
		result := report{}
		inspectCounterResponse([]byte(raw), &result)
		if result.TokenCount != nil {
			t.Fatal("unproven token counter accepted", raw)
		}
	}
}

func TestCounterTransportRejectsAlternateEndpointsAndRedirects(t *testing.T) {
	for _, url := range []string{
		"http://business.gemini.google/auth/getoxsrf?csesidx=x", "https://evil.invalid/auth/getoxsrf?csesidx=x",
		"https://business.gemini.google:443/auth/getoxsrf?csesidx=x", "https://business.gemini.google/auth/getoxsrf?csesidx=x&other=y",
		"https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/other", "https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetAddContextFile?other=y",
	} {
		called := false
		transport := &counterTransport{base: fakeTransport(func(*http.Request) (*http.Response, error) { called = true; return syntheticResponse(200, `{}`), nil }), result: &report{HTTPStatus: map[string]int{}}, sent: map[string]bool{}}
		request := httptest.NewRequest(http.MethodGet, url, nil)
		if _, err := transport.RoundTrip(request); err == nil || called {
			t.Fatal("alternate endpoint reached network", url)
		}
	}
	result := &report{HTTPStatus: map[string]int{}}
	calls := 0
	transport := &counterTransport{base: fakeTransport(func(*http.Request) (*http.Response, error) {
		calls++
		response := syntheticResponse(302, "")
		response.Header.Set("Location", "https://evil.invalid/private")
		return response, nil
	}), result: result, sent: map[string]bool{}}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("https://business.gemini.google/auth/getoxsrf?csesidx=x")
	if err != nil || response.StatusCode != 302 || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
	response.Body.Close()
}

func TestNoAllowLiveStopsBeforeAnyReadOrNetwork(t *testing.T) {
	var output bytes.Buffer
	if code := run([]string{"--vault", "nonexistent", "--file", "nonexistent", "--report", "nonexistent"}, &output); code != 2 {
		t.Fatal(code, output.String())
	}
	if strings.Contains(output.String(), "nonexistent") {
		t.Fatal("input paths echoed")
	}
}

func TestReportCannotOverwriteInputOrVault(t *testing.T) {
	root := t.TempDir()
	vaultPath := filepath.Join(root, "vault")
	input := filepath.Join(root, "input.txt")
	for _, reportPath := range []string{input, filepath.Join(vaultPath, "accounts.enc"), filepath.Join(vaultPath, "nested", "report.json")} {
		if separateReportPath(vaultPath, input, reportPath) {
			t.Fatal("report could overwrite a protected input")
		}
	}
	if !separateReportPath(vaultPath, input, filepath.Join(root, "report.json")) {
		t.Fatal("separate report path rejected")
	}
}
