package domain

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// AppSecretFileDir is the container directory holding file-delivered secrets.
const AppSecretFileDir = "/run/secrets"

// SecretFileEnvKey returns the env key that points to a file-delivered secret.
func SecretFileEnvKey(key string) string { return key + "_FILE" }

// SecretFilePath returns the container path of a file-delivered secret.
func SecretFilePath(key string) string { return AppSecretFileDir + "/" + key }

// SecretAsFile reports whether the secret env key is delivered as a file.
func (s AppService) SecretAsFile(key string) bool {
	return slices.Contains(s.SecretFiles, key)
}

// validateSecretFiles checks the file-delivered secret keys of one service.
func (s *AppService) validateSecretFiles() error {
	seen := make(map[string]struct{}, len(s.SecretFiles))
	for _, key := range s.SecretFiles {
		if _, ok := s.Secrets[key]; !ok {
			return fmt.Errorf("%w: service %q secret file key %q is not a declared secret", ErrInvalidAppSpec, s.Name, key)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: service %q secret file key %q is duplicated", ErrInvalidAppSpec, s.Name, key)
		}
		seen[key] = struct{}{}
		fileKey := SecretFileEnvKey(key)
		if _, ok := s.Secrets[fileKey]; ok {
			return fmt.Errorf("%w: service %q secret key %q collides with a secret file key", ErrInvalidAppSpec, s.Name, fileKey)
		}
		if _, ok := s.Env[fileKey]; ok {
			return fmt.Errorf("%w: service %q env key %q collides with a secret file key", ErrInvalidAppSpec, s.Name, fileKey)
		}
	}
	if len(s.SecretFiles) == 0 {
		return nil
	}
	for _, b := range s.Binds {
		if underSecretFileDir(b.Path) {
			return fmt.Errorf("%w: service %q bind %q must not mount at or under %s when secrets are delivered as files", ErrInvalidAppSpec, s.Name, b.Name, AppSecretFileDir)
		}
	}
	for _, v := range s.Volumes {
		if underSecretFileDir(v.Path) {
			return fmt.Errorf("%w: service %q volume %q must not mount at or under %s when secrets are delivered as files", ErrInvalidAppSpec, s.Name, v.Name, AppSecretFileDir)
		}
	}
	return nil
}

func underSecretFileDir(p string) bool {
	p = path.Clean(p)
	return p == AppSecretFileDir || strings.HasPrefix(p, AppSecretFileDir+"/")
}
