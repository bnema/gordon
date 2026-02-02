package registry

import "errors"

// ErrRepositoryNotFound indicates a registry repository does not exist.
var ErrRepositoryNotFound = errors.New("repository not found")
