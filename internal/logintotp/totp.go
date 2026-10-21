// Package logintotp implements the Google Authenticator TOTP profile.
package logintotp

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

func key(secret string) ([]byte, error) {
	// Match Manager's transient normalization; never rewrite the stored value.
	normalized := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(secret, " ", ""), "-", ""))
	if normalized == "" || len(normalized) > 16384 {
		return nil, errors.New("invalid TOTP secret")
	}
	if padding := len(normalized) % 8; padding != 0 {
		normalized += strings.Repeat("=", 8-padding)
	}
	decoded, err := base32.StdEncoding.DecodeString(normalized)
	if err != nil || len(decoded) == 0 {
		return nil, errors.New("invalid TOTP secret")
	}
	return decoded, nil
}

func Valid(secret string) bool { _, err := key(secret); return err == nil }

// Code uses a 30-second period, SHA-1, and exactly six digits, including zeros.
func Code(secret string, now time.Time) (string, error) {
	decoded, err := key(secret)
	if err != nil || now.Unix() < 0 {
		return "", errors.New("cannot generate TOTP")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, decoded)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 15
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000), nil
}

func Remaining(now time.Time) time.Duration {
	return 30*time.Second - time.Duration(now.UnixNano()%(30*int64(time.Second)))
}
