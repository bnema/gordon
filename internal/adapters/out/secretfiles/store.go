// Package secretfiles stores file-delivered app secrets on the host.
//
// Layout: <data_dir>/secrets-run/<app>/<container-name>/<KEY>, directories
// 0700 and files 0444. All access goes through an os.Root so symlinks
// cannot escape the base directory. Values and paths are never logged.
package secretfiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o444
	// maxContainerName bounds a container directory name.
	maxContainerName = 255
)

// Store implements out.SecretFileStore on the local filesystem.
type Store struct {
	dir  string
	root *os.Root
}

var _ out.SecretFileStore = (*Store)(nil)

// NewStore opens (creating if needed) <dataDir>/secrets-run.
func NewStore(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "secrets-run")
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("secret files: create base directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("secret files: stat base directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("secret files: base directory must be a directory, not a symlink")
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return nil, fmt.Errorf("secret files: chmod base directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("secret files: open base directory: %w", err)
	}
	return &Store{dir: dir, root: root}, nil
}

// Dir returns the absolute base directory.
func (s *Store) Dir() string { return s.dir }

// Close releases the base directory handle.
func (s *Store) Close() error { return s.root.Close() }

// Write replaces the container's directory with one 0444 file per key.
func (s *Store) Write(_ context.Context, app, container string, values map[string]string) (map[string]string, error) {
	if err := validateSet(app, container); err != nil {
		return nil, err
	}
	for key := range values {
		if err := domain.ValidateEnvKey(key); err != nil {
			return nil, fmt.Errorf("secret files: %w", err)
		}
	}
	if err := s.root.MkdirAll(app, dirMode); err != nil {
		return nil, fmt.Errorf("secret files: create app directory: %w", err)
	}
	if err := s.root.Chmod(app, dirMode); err != nil {
		return nil, fmt.Errorf("secret files: chmod app directory: %w", err)
	}
	rel := filepath.Join(app, container)
	if err := s.root.RemoveAll(rel); err != nil {
		return nil, fmt.Errorf("secret files: clear container directory: %w", err)
	}
	paths, err := s.writeFiles(rel, values)
	if err != nil {
		_ = s.root.RemoveAll(rel)
		return nil, err
	}
	return paths, nil
}

func (s *Store) writeFiles(rel string, values map[string]string) (map[string]string, error) {
	if err := s.root.Mkdir(rel, dirMode); err != nil {
		return nil, fmt.Errorf("secret files: create container directory: %w", err)
	}
	if err := s.root.Chmod(rel, dirMode); err != nil {
		return nil, fmt.Errorf("secret files: chmod container directory: %w", err)
	}
	paths := make(map[string]string, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		name := filepath.Join(rel, key)
		if err := s.writeFile(name, values[key]); err != nil {
			return nil, fmt.Errorf("secret files: write %s: %w", key, err)
		}
		paths[key] = filepath.Join(s.dir, name)
	}
	return paths, nil
}

func (s *Store) writeFile(name, value string) error {
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(value); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return s.root.Chmod(name, fileMode)
}

// Read returns one file's content.
func (s *Store) Read(_ context.Context, app, container, key string) (string, error) {
	if err := validateSet(app, container); err != nil {
		return "", err
	}
	if err := domain.ValidateEnvKey(key); err != nil {
		return "", fmt.Errorf("secret files: %w", err)
	}
	data, err := s.root.ReadFile(filepath.Join(app, container, key))
	if err != nil {
		return "", fmt.Errorf("secret files: read %s: %w", key, err)
	}
	return string(data), nil
}

// Remove deletes the container's directory and the app directory when it
// becomes empty. A missing directory is not an error.
func (s *Store) Remove(_ context.Context, app, container string) error {
	if err := validateSet(app, container); err != nil {
		return err
	}
	if err := s.root.RemoveAll(filepath.Join(app, container)); err != nil {
		return fmt.Errorf("secret files: remove container directory: %w", err)
	}
	if err := s.root.Remove(app); err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		return fmt.Errorf("secret files: remove app directory: %w", err)
	}
	return nil
}

// List returns every container directory, sorted by app then container.
func (s *Store) List(_ context.Context) ([]out.SecretFileSet, error) {
	fsys := s.root.FS()
	apps, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("secret files: list apps: %w", err)
	}
	var sets []out.SecretFileSet
	for _, app := range apps {
		if !app.IsDir() {
			continue
		}
		containers, err := fs.ReadDir(fsys, app.Name())
		if err != nil {
			return nil, fmt.Errorf("secret files: list containers: %w", err)
		}
		for _, c := range containers {
			if c.IsDir() {
				sets = append(sets, out.SecretFileSet{App: app.Name(), Container: c.Name()})
			}
		}
	}
	// fs.ReadDir returns entries sorted by name, so sets are already ordered.
	return sets, nil
}

func validateSet(app, container string) error {
	if err := domain.ValidateAppName(app); err != nil {
		return fmt.Errorf("secret files: %w", err)
	}
	if container == "" || len(container) > maxContainerName || container == "." || container == ".." ||
		filepath.Base(container) != container || strings.ContainsAny(container, "/\\\x00") {
		return errors.New("secret files: invalid container name")
	}
	return nil
}
