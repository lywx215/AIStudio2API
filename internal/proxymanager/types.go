// Package proxymanager owns the console's private proxy catalog and bounded jobs.
// It deliberately does not mutate the automatic global proxy pool.
package proxymanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }
func fail(code string) error   { return &Error{Code: code} }
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	return "PROXY_OPERATION_FAILED"
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random unavailable")
	}
	return hex.EncodeToString(b[:])
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type Source struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Protocol    string `json:"protocol"`
	Enabled     bool   `json:"enabled"`
	Revision    int64  `json:"revision"`
	Configured  bool   `json:"configured"`
	RefreshedAt string `json:"refreshed_at"`
	Error       string `json:"error"`
}
type SourceInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Protocol string `json:"protocol"`
	URL      string `json:"url"` // Write only. Never returned or formatted in diagnostics.
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}

func (SourceInput) String() string   { return "[private proxy source input]" }
func (SourceInput) GoString() string { return "[private proxy source input]" }

type Proxy struct {
	ID              string  `json:"id"`
	Protocol        string  `json:"protocol"`
	Endpoint        string  `json:"endpoint"`
	Enabled         bool    `json:"enabled"`
	Revision        int64   `json:"revision"`
	State           string  `json:"state"`
	ExitIP          string  `json:"exit_ip"`
	LatencyMS       float64 `json:"latency_ms"`
	StudioLatencyMS float64 `json:"studio_latency_ms"`
	CheckedAt       string  `json:"checked_at"`
	Error           string  `json:"error"`
	Present         bool    `json:"present"`
	Eligible        bool    `json:"eligible"`
	Sources         string  `json:"sources"`
	Accounts        int     `json:"accounts"`
}
type Query struct {
	Search   string `json:"search"`
	SourceID string `json:"source_id"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Eligible bool   `json:"eligible"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}
type Page struct {
	Items  []Proxy `json:"items"`
	Total  int     `json:"total"`
	Offset int     `json:"offset"`
	Limit  int     `json:"limit"`
}
type Stats struct {
	Total      int     `json:"total"`
	Passed     int     `json:"passed"`
	Failed     int     `json:"failed"`
	Restricted int     `json:"restricted"`
	Untested   int     `json:"untested"`
	Eligible   int     `json:"eligible"`
	AverageMS  float64 `json:"average_ms"`
}
type ProbeResult struct {
	State           string  `json:"state"`
	ExitIP          string  `json:"exit_ip"`
	LatencyMS       float64 `json:"latency_ms"`
	StudioLatencyMS float64 `json:"studio_latency_ms"`
	Error           string  `json:"error"`
}
type Account struct {
	ID                string `json:"id"`
	UUID              string `json:"account_id"`
	CredentialVersion uint64 `json:"credential_version"`
	StateVersion      uint64 `json:"state_version"`
	Archived          bool   `json:"archived"`
	Deleted           bool   `json:"deleted"`
	Enabled           bool   `json:"enabled"`
	Endpoint          string `json:"endpoint"`
	URL               string `json:"-"`
}
type Receipt struct {
	AccountID         string `json:"account_id"`
	CredentialVersion uint64 `json:"credential_version"`
	StateVersion      uint64 `json:"state_version"`
}

// AccountBackend contains no model verification or login capability.
type AccountBackend interface {
	Snapshot(context.Context, string) (Account, error)
	Apply(context.Context, Work) (Receipt, error)
	Committed(context.Context, string) (Receipt, bool, error)
	Counts(context.Context) (map[string]int, error)
}

// Work is private durable input. It must never be encoded into public job DTOs.
type Work struct {
	ProxyID       string
	ProxyRevision int64
	URL           string
	Account       Account
	OperationID   string
}

func (Work) String() string   { return "[private proxy operation input]" }
func (Work) GoString() string { return "[private proxy operation input]" }

type Target struct {
	ID                string `json:"id"`
	CredentialVersion uint64 `json:"credential_version"`
	StateVersion      uint64 `json:"state_version"`
}
type JobInput struct {
	Kind        string   `json:"kind"`
	IDs         []string `json:"ids"`
	All         bool     `json:"all"`
	Query       Query    `json:"query"`
	Concurrency int      `json:"concurrency"`
	Targets     []Target `json:"targets"`
	Mode        string   `json:"mode"`
	ProxyID     string   `json:"proxy_id"`
	SourceID    string   `json:"source_id"`
}
type Item struct {
	Index     int          `json:"index"`
	ProxyID   string       `json:"proxy_id"`
	Endpoint  string       `json:"endpoint"`
	AccountID string       `json:"account_id"`
	Status    string       `json:"status"`
	Code      string       `json:"code"`
	Probe     *ProbeResult `json:"probe,omitempty"`
	Receipt   *Receipt     `json:"receipt,omitempty"`
}
type Job struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	Total           int    `json:"total"`
	Done            int    `json:"done"`
	CancelRequested bool   `json:"cancel_requested"`
	Items           []Item `json:"items"`
	Offset          int    `json:"offset"`
	Limit           int    `json:"limit"`
}
