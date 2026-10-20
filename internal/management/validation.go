package management

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func invalid() error { return c.NewError(c.CodeInvalidRequest) }

// readObject enforces the bound before parsing, including requests without a
// Content-Length. Duplicate keys and null controls cannot acquire an alternate
// interpretation between admission, canonical identity, and the worker.
func readObject(w http.ResponseWriter, r *http.Request, optional bool) ([]byte, error) {
	raw, err := readBody(w, r, optional)
	if err != nil {
		return nil, err
	}
	if _, err := object(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func readBody(w http.ResponseWriter, r *http.Request, optional bool) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, c.MaxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := err.(*http.MaxBytesError); ok {
			return nil, c.NewError(c.CodePayloadTooLarge)
		}
		return nil, invalid()
	}
	if len(bytes.TrimSpace(raw)) == 0 && optional {
		raw = []byte("{}")
	}
	return raw, nil
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, invalid()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := uniqueValue(d); err != nil {
		return nil, invalid()
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, invalid()
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, invalid()
	}
	return obj, nil
}

func uniqueValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return invalid()
			}
			seen[s] = true
			if err := uniqueValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d); err != nil {
				return err
			}
		}
	default:
		return invalid()
	}
	_, err = d.Token()
	return err
}

// exactFields supplements encoding/json's case-insensitive field matching and
// its acceptance of null for non-pointer scalar controls.
func exactFields(raw []byte, typ reflect.Type) error {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return invalid()
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[c.Timestamp]() {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, err := object(raw)
		if err != nil {
			return err
		}
		fields := map[string]reflect.Type{}
		var add func(reflect.Type)
		add = func(t reflect.Type) {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if f.Anonymous {
					add(f.Type)
					continue
				}
				tag := strings.Split(f.Tag.Get("json"), ",")[0]
				if tag != "" && tag != "-" {
					fields[tag] = f.Type
				}
			}
		}
		add(typ)
		for name, value := range obj {
			t, ok := fields[name]
			if !ok {
				return invalid()
			}
			if err := exactFields(value, t); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return invalid()
		}
		for _, v := range values {
			if err := exactFields(v, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func decode(raw []byte, target any) error {
	if err := exactFields(raw, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return invalid()
	}
	return nil
}

func required(raw []byte, names ...string) error {
	obj, err := object(raw)
	if err != nil {
		return err
	}
	for _, name := range names {
		value, ok := obj[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid()
		}
	}
	return nil
}

func credentials(raw []byte) (c.Credentials, error) {
	var result c.Credentials
	if len(raw) > c.MaxCredentialBytes {
		return result, c.NewError(c.CodePayloadTooLarge)
	}
	if len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '"' {
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil {
			return result, c.NewError(c.CodeInvalidCredentials)
		}
		raw = []byte(encoded)
		if len(raw) > c.MaxCredentialBytes {
			return result, c.NewError(c.CodePayloadTooLarge)
		}
	}
	bad := func() (c.Credentials, error) { return c.Credentials{}, c.NewError(c.CodeInvalidCredentials) }
	if required(raw, "cookies", "origins") != nil || decode(raw, &result) != nil {
		return bad()
	}
	if result.Cookies == nil || result.Origins == nil {
		return bad()
	}
	var obj struct {
		Cookies []json.RawMessage
		Origins []json.RawMessage
	}
	if json.Unmarshal(raw, &obj) != nil {
		return bad()
	}
	for i, cookie := range result.Cookies {
		if required(obj.Cookies[i], "name", "value", "domain", "path", "expires", "httpOnly", "secure", "sameSite") != nil {
			return bad()
		}
		if cookie.Name == "" || cookie.Domain == "" || cookie.Path == "" || math.IsNaN(cookie.Expires) || math.IsInf(cookie.Expires, 0) {
			return bad()
		}
		if cookie.SameSite != "Strict" && cookie.SameSite != "Lax" && cookie.SameSite != "None" {
			return bad()
		}
	}
	for i, origin := range result.Origins {
		if required(obj.Origins[i], "origin", "localStorage") != nil || origin.LocalStorage == nil {
			return bad()
		}
		u, err := url.Parse(origin.Origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return bad()
		}
		var o struct{ LocalStorage []json.RawMessage }
		_ = json.Unmarshal(obj.Origins[i], &o)
		for _, item := range o.LocalStorage {
			if required(item, "name", "value") != nil {
				return bad()
			}
		}
	}
	return result, nil
}

func pageQuery(r *http.Request) (c.PageQuery, error) {
	q := c.PageQuery{Limit: c.DefaultPageLimit}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, invalid()
	}
	for name, v := range values {
		if (name != "offset" && name != "limit") || len(v) != 1 || v[0] == "" {
			return q, invalid()
		}
		for _, ch := range v[0] {
			if ch < '0' || ch > '9' {
				return q, invalid()
			}
		}
		n, err := strconv.ParseInt(v[0], 10, 53)
		if err != nil {
			return q, invalid()
		}
		if name == "offset" {
			q.Offset = int(n)
		} else {
			q.Limit = int(n)
		}
	}
	return q, q.Validate()
}

func accountIDs(ids []string) error {
	if len(ids) > c.MaxBatchItems {
		return c.NewError(c.CodePayloadTooLarge)
	}
	if len(ids) == 0 {
		return invalid()
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !c.ValidAccountID(id) || seen[id] {
			return invalid()
		}
		seen[id] = true
	}
	return nil
}

func clientRef(ref string) bool {
	return strings.TrimSpace(ref) == ref && ref != "" && utf8.RuneCountInString(ref) <= 128
}

func normalizePage[T any](p c.Page[T]) c.Page[T] {
	if p.Items == nil {
		p.Items = []T{}
	}
	return p
}
