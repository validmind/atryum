//go:build windows

package atryum

import "os"

// ownedByRoot is unused on Windows, where a managed install is refused.
func ownedByRoot(os.FileInfo) bool { return true }
