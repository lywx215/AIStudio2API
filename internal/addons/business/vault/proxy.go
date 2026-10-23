package vault

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Only schemes supported consistently by both Firefox and Go are accepted.
// Proxy authentication is not silently dropped: it is currently unsupported.
func ValidateProxy(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid proxy")
	}
	p, err := strconv.Atoi(u.Port())
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") || err != nil || p < 1 || p > 65535 || (u.Scheme != "http" && u.Scheme != "socks5") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return errors.New("invalid proxy")
	}
	return nil
}
