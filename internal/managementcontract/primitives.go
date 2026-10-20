package managementcontract

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

const TimestampLayout = "2006-01-02T15:04:05.000Z"

// Timestamp is always serialized as UTC with exactly three fractional digits.
// Optional times use *Timestamp and serialize as null when absent.
type Timestamp struct{ time.Time }

func NewTimestamp(value time.Time) Timestamp {
	return Timestamp{Time: value.UTC().Truncate(time.Millisecond)}
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.IsZero() || t.Year() < 1 || t.Year() > 9999 {
		return nil, fmt.Errorf("invalid management timestamp")
	}
	return json.Marshal(t.UTC().Truncate(time.Millisecond).Format(TimestampLayout))
}

func (t *Timestamp) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("invalid management timestamp")
	}
	parsed, err := time.Parse(TimestampLayout, value)
	if err != nil || parsed.Format(TimestampLayout) != value {
		return fmt.Errorf("invalid management timestamp")
	}
	*t = NewTimestamp(parsed)
	return nil
}

var opaqueID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,190}$`)
var uuid4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var modelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidID(value string) bool        { return opaqueID.MatchString(value) }
func ValidAccountID(value string) bool { return uuid4.MatchString(value) }
func ValidRequestID(value string) bool {
	return len(value) > 4 && value[:4] == "req_" && uuid4.MatchString(value[4:])
}
func ValidModel(value string) bool { return modelID.MatchString(value) }

// NewID creates a crypto-random lower-case UUIDv4, optionally prefixed by
// req, task, mkey, audit or op. Account identities use the empty prefix.
func NewID(prefix string) (string, error) {
	switch prefix {
	case "", "req", "task", "mkey", "audit", "op":
	default:
		return "", fmt.Errorf("invalid management ID prefix")
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	var buf [36]byte
	hex.Encode(buf[0:8], bytes[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], bytes[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], bytes[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], bytes[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], bytes[10:16])
	if prefix == "" {
		return string(buf[:]), nil
	}
	return prefix + "_" + string(buf[:]), nil
}

type PageQuery struct {
	Offset int
	Limit  int
}

func (q PageQuery) Validate() error {
	if q.Offset < 0 || q.Limit < 1 || q.Limit > MaxPageLimit {
		return NewError(CodeInvalidRequest)
	}
	return nil
}

type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

type Envelope[T any] struct {
	Data      T      `json:"data"`
	RequestID string `json:"requestId"`
}

type ErrorEnvelope struct {
	Error     *Error `json:"error"`
	RequestID string `json:"requestId"`
}
