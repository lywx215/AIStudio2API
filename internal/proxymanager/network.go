package proxymanager

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Downloader func(context.Context, string, func(context.Context, string) ([]byte, error)) ([]byte, error)

var providers = struct {
	sync.RWMutex
	entries map[string]Downloader
}{entries: map[string]Downloader{
	"webshare": func(ctx context.Context, address string, fetch func(context.Context, string) ([]byte, error)) ([]byte, error) {
		return fetch(ctx, address)
	},
	"text": func(ctx context.Context, address string, fetch func(context.Context, string) ([]byte, error)) ([]byte, error) {
		return fetch(ctx, address)
	},
}}

func RegisterDownloader(kind string, download Downloader) error {
	providers.Lock()
	defer providers.Unlock()
	if kind == "" || download == nil || providers.entries[kind] != nil {
		return fail("INVALID_DOWNLOADER")
	}
	providers.entries[kind] = download
	return nil
}
func hasDownloader(kind string) bool {
	providers.RLock()
	defer providers.RUnlock()
	return providers.entries[kind] != nil
}
func sourceDownload(ctx context.Context, kind, address string, fetch func(context.Context, string) ([]byte, error)) ([]byte, error) {
	providers.RLock()
	loader := providers.entries[kind]
	providers.RUnlock()
	if loader == nil {
		return nil, fail("UNSUPPORTED_SOURCE")
	}
	return loader(ctx, address, fetch)
}
func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(prefix).Contains(a) {
			return false
		}
	}
	return true
}
func validateDownloadURL(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(address) > 8192 {
		return fail("INVALID_DOWNLOAD_URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if !publicIP(ip) {
			return fail("PRIVATE_DOWNLOAD_ADDRESS")
		}
	} else if !validHostname(strings.ToLower(u.Hostname())) {
		return fail("INVALID_DOWNLOAD_URL")
	}
	return nil
}

// Resolve and pin each actual socket connection, including HTTP/SOCKS proxy
// sockets. Environment proxies, private DNS answers and rebinding are excluded.
func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fail("INVALID_NETWORK_ADDRESS")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fail("DNS_FAILED")
	}
	if len(addresses) == 0 {
		return nil, fail("DNS_FAILED")
	}
	for _, a := range addresses {
		if !publicIP(a.IP) {
			return nil, fail("PRIVATE_NETWORK_ADDRESS")
		}
	}
	var last error
	for _, a := range addresses {
		connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return connection, nil
		}
		last = err
	}
	return nil, last
}
func transport(proxyURL *url.URL) *http.Transport {
	return &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return proxyURL, nil }, DialContext: publicDial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 12 * time.Second, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10}
}
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
func Download(ctx context.Context, address string) ([]byte, error) {
	if err := validateDownloadURL(address); err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport(nil), CheckRedirect: noRedirect}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fail("INVALID_DOWNLOAD_URL")
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fail(networkCode(err))
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return nil, fail("DOWNLOAD_REDIRECT_REJECTED")
		}
		return nil, fail("DOWNLOAD_HTTP_FAILED")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (5<<20)+1))
	if err != nil {
		return nil, fail("DOWNLOAD_READ_FAILED")
	}
	if len(data) > 5<<20 {
		return nil, fail("LIST_TOO_LARGE")
	}
	return data, nil
}
func networkCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "TIMEOUT"
	}
	var cert *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	if errors.As(err, &cert) || errors.As(err, &authority) {
		return "TLS_FAILED"
	}
	// Only the closed classification escapes this function; URL/auth-bearing
	// transport error strings are never returned or logged.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "authentication") || strings.Contains(message, "407") || strings.Contains(message, "username") || strings.Contains(message, "password") {
		return "PROXY_AUTH_FAILED"
	}
	if strings.Contains(message, "tls") {
		return "TLS_FAILED"
	}
	return "CONNECT_FAILED"
}
func Probe(ctx context.Context, proxy string) ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u, err := url.Parse(proxy)
	if err != nil {
		return ProbeResult{State: "failed", Error: "INVALID_PROXY"}
	}
	client := &http.Client{Transport: transport(u), CheckRedirect: noRedirect}
	defer client.CloseIdleConnections()
	return probeWithClient(ctx, client, "https://api.ipify.org?format=json", "https://aistudio.google.com/")
}
func probeWithClient(ctx context.Context, client *http.Client, ipTarget, studioTarget string) ProbeResult {
	result := ProbeResult{State: "failed"}
	started := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ipTarget, nil)
	response, err := client.Do(req)
	if err != nil {
		result.Error = networkCode(err)
		return result
	}
	result.LatencyMS = float64(time.Since(started).Microseconds()) / 1000
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 {
		result.State = "restricted"
		result.Error = "IP_HTTP_RESTRICTED"
		return result
	}
	var ip struct {
		IP string `json:"ip"`
	}
	if response.StatusCode != 200 || readErr != nil || len(data) > 4096 || json.Unmarshal(data, &ip) != nil || !publicIP(net.ParseIP(ip.IP)) {
		result.Error = "INVALID_EXIT_IP_RESPONSE"
		return result
	}
	result.ExitIP = ip.IP
	started = time.Now()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, studioTarget, nil)
	response, err = client.Do(req)
	if err != nil {
		result.Error = networkCode(err)
		return result
	}
	defer response.Body.Close()
	result.StudioLatencyMS = float64(time.Since(started).Microseconds()) / 1000
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 {
		result.State = "restricted"
		result.Error = "AISTUDIO_HTTP_RESTRICTED"
		return result
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		location, err := response.Location()
		if err != nil || location.Scheme != "https" || !(location.Hostname() == "google.com" || strings.HasSuffix(location.Hostname(), ".google.com")) {
			result.Error = "AISTUDIO_UNEXPECTED_REDIRECT"
			return result
		}
	}
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		result.State = "passed"
		return result
	}
	result.Error = "AISTUDIO_HTTP_FAILED"
	return result
}
