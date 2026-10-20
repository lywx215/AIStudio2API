package management

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

type rejectedBodyCounter struct {
	io.Reader
	read int64
}

func (body *rejectedBodyCounter) Read(p []byte) (int, error) {
	n, err := body.Reader.Read(p)
	body.read += int64(n)
	return n, err
}
func (*rejectedBodyCounter) Close() error { return nil }

type rejectionDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines    []time.Time
	beforeHeader func()
}

func (w *rejectionDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
func (w *rejectionDeadlineRecorder) WriteHeader(status int) {
	if w.beforeHeader != nil {
		w.beforeHeader()
	}
	w.ResponseRecorder.WriteHeader(status)
}

// A recorder normally cannot expose deadline support. This deterministic body
// counter verifies the drain happens before the first response byte and never
// reads an entire oversized upload merely to reject it.
func TestHTTPRejectedBodyDrainBoundsBeforeResponse(t *testing.T) {
	for _, size := range []int{42, int(rejectedBodyDrainBytes) + 128} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			h, store, _, _ := testHTTP(t)
			body := &rejectedBodyCounter{Reader: strings.NewReader(strings.Repeat("x", size))}
			r := httptest.NewRequest(http.MethodPost, "http://management.test"+c.KeyNamespace, nil)
			r.Body, r.ContentLength = body, int64(size)
			r.Header.Set("Origin", "http://attacker.test")
			r.Header.Set("X-Requested-With", "XMLHttpRequest")
			expectedRead := int64(size)
			if expectedRead > rejectedBodyDrainBytes {
				expectedRead = rejectedBodyDrainBytes + 1
			}
			w := &rejectionDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			w.beforeHeader = func() {
				if body.read != expectedRead {
					t.Errorf("request read before response=%d, want %d", body.read, expectedRead)
				}
			}
			h.KeyHandler(c.ConsoleAuthorizeFunc(func(*http.Request) error { return nil })).ServeHTTP(w, r)
			assertEnvelope(t, w.ResponseRecorder)
			assertCode(t, w.ResponseRecorder, c.CodeForbidden)
			if body.read != expectedRead || len(w.deadlines) != 2 || w.deadlines[0].IsZero() {
				t.Fatalf("unbounded drain or missing deadline cleanup: read=%d deadlines=%v", body.read, w.deadlines)
			}
			if size > int(rejectedBodyDrainBytes) && w.Header().Get("Connection") != "close" {
				t.Fatal("oversized rejected upload kept its connection reusable")
			}
			if store.keyCreates != 0 {
				t.Fatal("rejected upload created a management key")
			}
		})
	}
}

func managementWireConnection(t *testing.T, server *httptest.Server) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func managementWireResponse(t *testing.T, reader *bufio.Reader, method string) (*http.Response, *httptest.ResponseRecorder) {
	t.Helper()
	response, err := http.ReadResponse(reader, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read complete rejection body: %v", err)
	}
	_ = response.Body.Close()
	if response.ContentLength != int64(len(body)) {
		t.Fatalf("response length=%d actual=%d", response.ContentLength, len(body))
	}
	recorder := httptest.NewRecorder()
	recorder.Code = response.StatusCode
	recorder.HeaderMap = response.Header.Clone()
	_, _ = recorder.Body.Write(body)
	assertEnvelope(t, recorder)
	return response, recorder
}

// Exercise real TCP rather than ResponseRecorder: urllib-style closing POSTs
// used to race server closure with their unread body on Windows.
func TestHTTPRejectedRequestsCompleteBeforeClose(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, headers string
		console, authorized         bool
		code                        c.ErrorCode
	}{
		{"cross site", "POST", c.KeyNamespace, "Origin: http://attacker.test\r\nX-Requested-With: XMLHttpRequest\r\n", true, true, c.CodeForbidden},
		{"missing ajax", "POST", c.KeyNamespace, "", true, true, c.CodeForbidden},
		{"missing console session", "POST", c.KeyNamespace, "", true, false, c.CodeConsolePasswordRequired},
		{"missing management bearer", "POST", c.Namespace + "/accounts/import", "", false, false, c.CodeUnauthorized},
		{"wrong method", "PUT", c.KeyNamespace, "", true, true, c.CodeMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _, tasks := testHTTP(t)
			handler := h.Handler()
			if tc.console {
				handler = h.KeyHandler(c.ConsoleAuthorizeFunc(func(*http.Request) error {
					if !tc.authorized {
						return c.NewError(c.CodeConsolePasswordRequired)
					}
					return nil
				}))
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			for attempt := 0; attempt < 5; attempt++ {
				conn := managementWireConnection(t, server)
				body := `{"name":"rejected","scopes":["system:read"]}`
				_, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: management.test\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: %d\r\n%s\r\n%s", tc.method, tc.path, len(body), tc.headers, body)
				if err != nil {
					t.Fatal(err)
				}
				response, recorder := managementWireResponse(t, bufio.NewReader(conn), tc.method)
				assertCode(t, recorder, tc.code)
				if !response.Close {
					t.Fatal("closing request did not close its response")
				}
				_ = conn.Close()
			}
			if store.keyCreates != 0 || len(tasks.calls) != 0 {
				t.Fatal("early rejection performed a mutation")
			}
		})
	}
}

func TestHTTPRejectedBodyDrainTimeAndSizeOverTCP(t *testing.T) {
	for _, tc := range []struct{ name, headers, body string }{
		{"oversized", fmt.Sprintf("Content-Length: %d\r\n", 4*rejectedBodyDrainBytes), strings.Repeat("x", int(rejectedBodyDrainBytes)+1)},
		{"slow sender", "Content-Length: 32\r\n", ""},
		{"expect continue", "Content-Length: 32\r\nExpect: 100-continue\r\n", ""},
		{"chunked", "Transfer-Encoding: chunked\r\n", "2\r\n{}\r\n0\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _, _ := testHTTP(t)
			server := httptest.NewServer(h.KeyHandler(c.ConsoleAuthorizeFunc(func(*http.Request) error { return nil })))
			defer server.Close()
			conn := managementWireConnection(t, server)
			started := time.Now()
			_, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: management.test\r\nConnection: close\r\nOrigin: http://attacker.test\r\nX-Requested-With: XMLHttpRequest\r\n%s\r\n%s", c.KeyNamespace, tc.headers, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			_, recorder := managementWireResponse(t, bufio.NewReader(conn), http.MethodPost)
			assertCode(t, recorder, c.CodeForbidden)
			if time.Since(started) > 2*time.Second {
				t.Fatal("rejected upload exceeded its bounded drain time")
			}
			if store.keyCreates != 0 {
				t.Fatal("rejected body created a management key")
			}
			_ = conn.Close()
		})
	}
}

func TestHTTPRejectedSmallBodyKeepsConnectionReusable(t *testing.T) {
	h, store, _, _ := testHTTP(t)
	server := httptest.NewServer(h.KeyHandler(c.ConsoleAuthorizeFunc(func(*http.Request) error { return nil })))
	defer server.Close()
	conn := managementWireConnection(t, server)
	reader := bufio.NewReader(conn)
	body := `{"name":"rejected","scopes":["system:read"]}`
	if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: management.test\r\nOrigin: http://attacker.test\r\nX-Requested-With: XMLHttpRequest\r\nContent-Length: %d\r\n\r\n%s", c.KeyNamespace, len(body), body); err != nil {
		t.Fatal(err)
	}
	response, recorder := managementWireResponse(t, reader, http.MethodPost)
	assertCode(t, recorder, c.CodeForbidden)
	if response.Close {
		t.Fatal("fully drained small rejection unnecessarily closed the connection")
	}
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: management.test\r\nConnection: close\r\n\r\n", c.KeyNamespace); err != nil {
		t.Fatal(err)
	}
	_, recorder = managementWireResponse(t, reader, http.MethodGet)
	if recorder.Code != http.StatusOK || store.keyCreates != 0 {
		t.Fatalf("subsequent request=%d key mutations=%d", recorder.Code, store.keyCreates)
	}
}
