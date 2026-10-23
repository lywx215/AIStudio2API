// Package observe stores passive observations, never generation verification.
package observe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const AdapterVersion = "business-observer-v1"

var ErrConflict = errors.New("observation revision or scope changed")
var ErrInvalid = errors.New("invalid or unavailable observation")

type Scope struct {
	AccountID       string `json:"account_id"`
	CredentialEpoch uint64 `json:"credential_epoch"`
	ConfigID        string `json:"config_id"`
}
type Source struct {
	Kind string
	URL  string
}
type Observation struct {
	DisplayName string    `json:"display_name"`
	UpstreamID  string    `json:"upstream_id"`
	AutoForm    string    `json:"auto_form,omitempty"`
	Source      string    `json:"source"`
	SourceURL   string    `json:"source_url"`
	ObservedAt  time.Time `json:"observed_at"`
}
type Entry struct {
	Scope
	Observation
	ID             string    `json:"id"`
	ExpiresAt      time.Time `json:"expires_at"`
	AdapterVersion string    `json:"adapter_version"`
	State          string    `json:"state"`
}
type Snapshot struct {
	Revision uint64  `json:"revision"`
	Entries  []Entry `json:"entries"`
}
type Catalog struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

func New(path string) *Catalog { return &Catalog{path: path, now: time.Now} }
func validText(s string) bool  { return len(s) <= 256 && !strings.ContainsAny(s, "\r\n\x00") }
func cleanURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.User != nil || (u.Host != "business.gemini.google" && u.Host != "biz-discoveryengine.googleapis.com") {
		return "", ErrInvalid
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// Only explicitly observed wire fields are parsed. In particular absent/empty
// modelId is recorded as unresolved; it does not prove that the user chose Auto.
func Parse(scope Scope, source Source, payload []byte, at time.Time) ([]Observation, error) {
	address, err := cleanURL(source.URL)
	if err != nil || len(payload) > 256<<10 || scope.AccountID == "" || scope.CredentialEpoch == 0 || scope.ConfigID == "" {
		return nil, ErrInvalid
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil {
		return nil, ErrInvalid
	}
	if config, ok := root["configId"]; ok {
		var id string
		if json.Unmarshal(config, &id) != nil || id != scope.ConfigID {
			return nil, ErrInvalid
		}
	}
	result := []Observation{}
	add := func(name, id, form string) {
		if validText(name) && validText(id) {
			result = append(result, Observation{name, id, form, source.Kind, address, at.UTC()})
		}
	}
	switch source.Kind {
	case "model_config":
		var wrapper struct {
			UI struct {
				Info struct {
					Models []struct {
						ID   string `json:"modelId"`
						Name string `json:"displayName"`
					} `json:"resolvedModels"`
				} `json:"modelConfigInfo"`
			} `json:"uiSettings"`
		}
		data := payload
		if widget, ok := root["widgetConfig"]; ok {
			data = widget
		}
		if json.Unmarshal(data, &wrapper) != nil || len(wrapper.UI.Info.Models) > 128 {
			return nil, ErrInvalid
		}
		for _, model := range wrapper.UI.Info.Models {
			if model.ID != "" {
				add(model.Name, model.ID, "")
			}
		}
	case "user_request":
		var assist map[string]json.RawMessage
		if json.Unmarshal(root["streamAssistRequest"], &assist) != nil || assist == nil {
			return nil, ErrInvalid
		}
		generation, exists := assist["assistGenerationConfig"]
		if !exists {
			add("", "", "generation_config_omitted")
			break
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(generation, &fields) != nil || fields == nil {
			return nil, ErrInvalid
		}
		model, exists := fields["modelId"]
		if !exists {
			add("", "", "model_id_omitted")
			break
		}
		var id string
		if string(model) == "null" || json.Unmarshal(model, &id) != nil {
			return nil, ErrInvalid
		}
		if id == "" {
			add("", "", "model_id_empty")
		} else {
			add("", id, "")
		}
	default:
		return nil, ErrInvalid
	}
	return result, nil
}

func (c *Catalog) load() (Snapshot, error) {
	f, err := os.Open(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Entries: []Entry{}}, nil
	}
	if err != nil {
		return Snapshot{}, ErrInvalid
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	var s Snapshot
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, &s) != nil || len(s.Entries) > 1024 {
		return Snapshot{}, ErrInvalid
	}
	if s.Entries == nil {
		s.Entries = []Entry{}
	}
	return s, nil
}
func (c *Catalog) update(fn func(*Snapshot) error) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if os.MkdirAll(filepath.Dir(c.path), 0700) != nil {
		return Snapshot{}, ErrInvalid
	}
	lock := flock.New(c.path + ".lock")
	locked, err := lock.TryLock()
	defer lock.Close()
	if err != nil || !locked {
		return Snapshot{}, ErrConflict
	}
	s, err := c.load()
	if err != nil {
		return s, err
	}
	before, _ := json.Marshal(s)
	if err = fn(&s); err != nil {
		return Snapshot{}, err
	}
	after, _ := json.Marshal(s)
	if string(before) == string(after) {
		return s, nil
	}
	s.Revision++
	data, err := json.Marshal(s)
	if err != nil || len(data) > 4<<20 {
		return Snapshot{}, ErrInvalid
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".observations-*.tmp")
	if err != nil {
		return Snapshot{}, ErrInvalid
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), c.path)
	}
	if err != nil {
		return Snapshot{}, ErrInvalid
	}
	return s, nil
}
func (c *Catalog) List() (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, err := c.load()
	for i := range s.Entries {
		if s.Entries[i].State != "stale" && !c.now().Before(s.Entries[i].ExpiresAt) {
			s.Entries[i].State = "expired"
		}
	}
	return s, err
}
func (c *Catalog) ReconcileScopes(scopes []Scope) (Snapshot, error) {
	return c.update(func(s *Snapshot) error {
		for i := range s.Entries {
			e := &s.Entries[i]
			found := false
			for _, scope := range scopes {
				if e.Scope == scope {
					found = true
					break
				}
			}
			if !found {
				e.State = "stale"
			}
		}
		return nil
	})
}
func (c *Catalog) Record(scope Scope, items []Observation) (Snapshot, error) {
	if len(items) > 128 || scope.AccountID == "" || scope.ConfigID == "" || scope.CredentialEpoch == 0 {
		return Snapshot{}, ErrInvalid
	}
	return c.update(func(s *Snapshot) error {
		for _, item := range items {
			address, err := cleanURL(item.SourceURL)
			if err != nil || !validText(item.DisplayName) || !validText(item.UpstreamID) || (item.Source != "model_config" && item.Source != "user_request") || item.ObservedAt.IsZero() || item.ObservedAt.After(c.now().Add(time.Minute)) {
				return ErrInvalid
			}
			item.SourceURL = address
			identity, _ := json.Marshal([]any{scope, item.DisplayName, item.UpstreamID, item.AutoForm, item.Source, address})
			hash := sha256.Sum256(identity)
			id := hex.EncodeToString(hash[:16])
			index := -1
			count := 0
			for i, e := range s.Entries {
				if e.Scope == scope {
					if e.State == "stale" {
						return ErrConflict
					}
					count++
				}
				if e.ID == id {
					index = i
				}
			}
			if index < 0 {
				if len(s.Entries) >= 1024 || count >= 128 {
					return ErrInvalid
				}
				s.Entries = append(s.Entries, Entry{Scope: scope, ID: id})
				index = len(s.Entries) - 1
			}
			e := &s.Entries[index]
			state := "pending"
			if e.State == "confirmed" && c.now().Before(e.ExpiresAt) {
				state = "confirmed"
			}
			*e = Entry{scope, item, id, item.ObservedAt.Add(7 * 24 * time.Hour), AdapterVersion, state}
		}
		for i := range s.Entries {
			a := &s.Entries[i]
			if a.Scope != scope || a.DisplayName == "" || a.State == "stale" || !c.now().Before(a.ExpiresAt) {
				continue
			}
			for j := range s.Entries {
				b := &s.Entries[j]
				if a.Scope == b.Scope && a.DisplayName == b.DisplayName && a.UpstreamID != b.UpstreamID && b.State != "stale" && c.now().Before(b.ExpiresAt) {
					a.State, b.State = "conflict", "conflict"
				}
			}
		}
		return nil
	})
}
func (c *Catalog) Confirm(revision uint64, id string, scope Scope) (Snapshot, error) {
	return c.update(func(s *Snapshot) error {
		if s.Revision != revision {
			return ErrConflict
		}
		for i := range s.Entries {
			e := &s.Entries[i]
			if e.ID == id {
				if e.Scope != scope || e.State != "pending" || e.UpstreamID == "" || !c.now().Before(e.ExpiresAt) {
					return ErrInvalid
				}
				e.State = "confirmed"
				return nil
			}
		}
		return ErrInvalid
	})
}
