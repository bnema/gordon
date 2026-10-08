package out

import "context"

// SecretFileSet identifies one container's secret file directory.
type SecretFileSet struct{ App, Container string }

// SecretFileStore persists per-container secret files on the host. Values
// cross this boundary in memory only; nothing is logged.
type SecretFileStore interface {
	// Write replaces the container's directory with one 0444 file per key
	// and returns key -> absolute host path.
	Write(ctx context.Context, app, container string, values map[string]string) (map[string]string, error)
	// Read returns one file's content.
	Read(ctx context.Context, app, container, key string) (string, error)
	// Remove deletes the container's directory (missing is not an error) and
	// the app directory when it becomes empty.
	Remove(ctx context.Context, app, container string) error
	// List returns every existing container directory, sorted by app then container.
	List(ctx context.Context) ([]SecretFileSet, error)
}
