//go:build !windows

package aistudio

import "os"

func accountPathIsReparse(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }
