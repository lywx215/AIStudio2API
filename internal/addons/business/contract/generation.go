package contract

import (
	"context"
	"encoding/json"
	"errors"
)

// Request retains original field presence for validation. Only validated fields
// are mapped into QueryParts / Generation before an account can be leased.
type Request struct {
	Raw         map[string]json.RawMessage
	QueryParts  []map[string]string
	Generation  map[string]json.RawMessage
	Attachments []Attachment
}

// Attachment contains an immutable request-owned snapshot. The account lease
// uploads it only after a fresh Business session has been selected. No upstream
// file ID survives across requests, account epochs or session boundaries.
type Attachment struct {
	Filename string
	MIMEType string
	Hash     string
	Data     []byte
}

type FileResolver func(context.Context, string, string) (Attachment, error)

type Selection struct {
	ModelID    string
	Auto       bool
	AccountIDs []string
}
type Part struct {
	Text             string      `json:"text"`
	Thought          bool        `json:"thought,omitempty"`
	ThoughtSignature string      `json:"thoughtSignature,omitempty"`
	InlineData       *InlineData `json:"inlineData,omitempty"`
}

const SuggestionsMIMEType = "application/json+suggestions"

// InlineData owns decoded bytes. The standard JSON encoder writes data using
// padded standard base64, as required by the native Gemini Blob wire format.
type InlineData struct {
	MIMEType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

// MarshalJSON preserves the native Part union, including an explicitly empty
// text part. An inlineData part must not acquire an invented text:"" sibling.
func (p Part) MarshalJSON() ([]byte, error) {
	if p.InlineData == nil {
		type plain Part
		return json.Marshal(plain(p))
	}
	if p.Text != "" {
		return nil, errors.New("Business part contains multiple content values")
	}
	return json.Marshal(struct {
		InlineData       *InlineData `json:"inlineData"`
		Thought          bool        `json:"thought,omitempty"`
		ThoughtSignature string      `json:"thoughtSignature,omitempty"`
	}{p.InlineData, p.Thought, p.ThoughtSignature})
}

type Event struct {
	Parts    []Part
	Finished bool
}

type Generator interface {
	Generate(context.Context, Selection, Request, func(Event) error) error
}

// Failure is safe to expose; no underlying transport error or response body is
// included. Submitted operations are not retried automatically.
type Failure struct {
	Code            int
	Status, Message string
}

func (e *Failure) Error() string                  { return e.Message }
func Fail(code int, status, message string) error { return &Failure{code, status, message} }
