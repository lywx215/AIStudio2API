//go:build !windows

package vault

// Unix installations protect the root key with directory 0700 / file 0600.
// Copying the whole runtime directory therefore also copies decryption access.
func wrapKey(data []byte) ([]byte, error) { return append([]byte(nil), data...), nil }
func unwrapKey(data []byte) ([]byte, error) {
	if len(data) != 32 {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), data...), nil
}
