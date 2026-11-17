// Package sharedagenthook embeds the shared agent hook script so the atryum
// binary can install it without reading it from the file system.
package sharedagenthook

import _ "embed"

// Script is atryum-hook.mjs as it was when the binary was built.
//
//go:embed atryum-hook.mjs
var Script []byte
