//go:build windows

package vault

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

func wrapKey(data []byte) ([]byte, error)   { return protectKey(data, true) }
func unwrapKey(data []byte) ([]byte, error) { return protectKey(data, false) }
func protectKey(data []byte, encrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrUnavailable
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	result := append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...)
	if !encrypt && len(result) != 32 {
		clear(result)
		return nil, ErrUnavailable
	}
	return result, nil
}
