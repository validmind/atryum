// Package version holds the atryum build version.
package version

// Version is "dev" for local builds. Release builds inject the release tag
// via -ldflags "-X github.com/validmind/atryum/internal/version.Version=<tag>".
var Version = "dev"
