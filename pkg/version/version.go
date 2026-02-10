// Package version holds build-time version info for Gordon.
// Set via main using Set(), read from anywhere via Get().
package version

// Build information, populated by Set() at startup.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// Set stores build-time version info. Call once from main.
func Set(v, c, d string) {
	version = v
	commit = c
	buildDate = d
}

// Version returns the build version string.
func Version() string { return version }

// Commit returns the build commit hash.
func Commit() string { return commit }

// BuildDate returns the build date string.
func BuildDate() string { return buildDate }
