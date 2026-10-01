// Package version holds build-time version info for Gordon.
// Set via main using Set(), read from anywhere via Version(), Commit(),
// BuildDate(), or Dirty().
package version

// Build information, populated by Set() at startup.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
	dirty     = "unknown"
)

// Set stores build-time version info. Call once from main.
func Set(v, c, d, isDirty string) {
	version = v
	commit = c
	buildDate = d
	dirty = isDirty
}

// Version returns the build version string.
func Version() string { return version }

// Commit returns the build commit hash.
func Commit() string { return commit }

// BuildDate returns the build date string.
func BuildDate() string { return buildDate }

// Dirty reports whether the source checkout had uncommitted changes.
func Dirty() string { return dirty }
