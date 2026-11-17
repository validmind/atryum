//go:build !windows

package atryum

import (
	"os"
	"syscall"
)

// ownedByRoot reports whether the file's owner is root.
func ownedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
