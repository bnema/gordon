// Package domainsecrets implements the DomainSecretStore adapter using pass.
package domainsecrets

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// ansiRegex matches ANSI escape sequences for stripping from pass output.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const (
	passDomainSecretsPath = "gordon/env"             //nolint:gosec // Not a credential, this is a pass store path.
	passAttachmentPath    = "gordon/env/attachments" //nolint:gosec // Not a credential, this is a pass store path.
)

// PassStore implements the DomainSecretStore interface using the pass password manager.
type PassStore struct {
	timeout time.Duration
	log     zerowrap.Logger
}

// NewPassStore creates a new pass-based domain secret store.
func NewPassStore(log zerowrap.Logger) (*PassStore, error) {
	if err := exec.Command("pass", "version").Run(); err != nil {
		return nil, fmt.Errorf("pass is not available: %w", err)
	}

	log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "domainsecrets").
		Str("provider", "pass").
		Msg("domain secret store initialized")

	return &PassStore{
		timeout: 10 * time.Second,
		log:     log,
	}, nil
}

// ListKeys returns the list of secret keys for a domain (not values).
func (s *PassStore) ListKeys(domainName string) ([]string, error) {
	manifestPath, err := s.manifestPath(domainName)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	content, exists, err := s.passShow(ctx, manifestPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []string{}, nil
	}

	keys := []string{}
	for _, line := range strings.Split(content, "\n") {
		key := strings.TrimSpace(line)
		if key == "" {
			continue
		}
		keys = append(keys, key)
	}

	return keys, nil
}

// GetAll returns all secrets for a domain as a key-value map.
func (s *PassStore) GetAll(domainName string) (map[string]string, error) {
	keys, err := s.ListKeys(domainName)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	secretsMap := make(map[string]string)
	for _, key := range keys {
		keyPath, err := s.keyPath(domainName, key)
		if err != nil {
			return nil, err
		}

		value, exists, err := s.passShow(ctx, keyPath)
		if err != nil {
			return nil, err
		}
		if !exists {
			s.log.Warn().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "domainsecrets").
				Str("domain", domainName).
				Str("key", key).
				Msg("secret listed in manifest but missing in pass")
			continue
		}
		secretsMap[key] = value
	}

	return secretsMap, nil
}

// Set sets or updates multiple secrets for a domain.
func (s *PassStore) Set(domainName string, secretsMap map[string]string) error {
	if _, err := s.domainPath(domainName); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	for key, value := range secretsMap {
		keyPath, err := s.keyPath(domainName, key)
		if err != nil {
			return err
		}
		if err := s.passInsert(ctx, keyPath, value); err != nil {
			return fmt.Errorf("failed to store secret %s: %w", key, err)
		}
	}

	existingKeys, err := s.ListKeys(domainName)
	if err != nil {
		return err
	}

	keySet := make(map[string]struct{}, len(existingKeys)+len(secretsMap))
	for _, key := range existingKeys {
		keySet[key] = struct{}{}
	}
	for key := range secretsMap {
		keySet[key] = struct{}{}
	}

	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	manifestPath, err := s.manifestPath(domainName)
	if err != nil {
		return err
	}

	if err := s.passInsert(ctx, manifestPath, strings.Join(keys, "\n")); err != nil {
		return fmt.Errorf("failed to update manifest: %w", err)
	}

	return nil
}

// Delete removes a specific secret key from a domain.
func (s *PassStore) Delete(domainName, key string) error {
	if _, err := s.domainPath(domainName); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	keyPath, err := s.keyPath(domainName, key)
	if err != nil {
		return err
	}
	if err := s.passRemove(ctx, keyPath); err != nil {
		return err
	}

	keys, err := s.ListKeys(domainName)
	if err != nil {
		return err
	}

	updated := make([]string, 0, len(keys))
	for _, existingKey := range keys {
		if existingKey == key {
			continue
		}
		updated = append(updated, existingKey)
	}
	sort.Strings(updated)

	manifestPath, err := s.manifestPath(domainName)
	if err != nil {
		return err
	}

	if err := s.passInsert(ctx, manifestPath, strings.Join(updated, "\n")); err != nil {
		return fmt.Errorf("failed to update manifest: %w", err)
	}

	return nil
}

// ListAttachmentKeys finds attachment secrets for a domain from pass.
func (s *PassStore) ListAttachmentKeys(domainName string) ([]out.AttachmentSecrets, error) {
	if _, err := s.domainPath(domainName); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	containers, err := s.listTopLevelEntries(ctx, passAttachmentPath)
	if err != nil {
		return nil, err
	}

	sanitizedDomain := sanitizeDomainForContainer(domainName)
	prefix := "gordon-" + sanitizedDomain + "-"

	var results []out.AttachmentSecrets
	for _, containerName := range containers {
		if !strings.HasPrefix(containerName, prefix) {
			continue
		}

		manifestPath := fmt.Sprintf("%s/%s/.keys", passAttachmentPath, containerName)
		if err := secrets.ValidatePath(manifestPath); err != nil {
			return nil, err
		}

		content, exists, err := s.passShow(ctx, manifestPath)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}

		keys := []string{}
		for _, line := range strings.Split(content, "\n") {
			key := strings.TrimSpace(line)
			if key == "" {
				continue
			}
			keys = append(keys, key)
		}

		if len(keys) > 0 {
			results = append(results, out.AttachmentSecrets{
				Service: containerName,
				Keys:    keys,
			})
		}
	}

	return results, nil
}

func (s *PassStore) domainPath(domainName string) (string, error) {
	safeDomain, err := s.sanitizeDomain(domainName)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/%s", passDomainSecretsPath, safeDomain)
	if err := secrets.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *PassStore) keyPath(domainName, key string) (string, error) {
	domainPath, err := s.domainPath(domainName)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/%s", domainPath, key)
	if err := secrets.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *PassStore) manifestPath(domainName string) (string, error) {
	domainPath, err := s.domainPath(domainName)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/.keys", domainPath)
	if err := secrets.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *PassStore) sanitizeDomain(domainName string) (string, error) {
	safeDomain, err := domain.SanitizeDomainForEnvFile(domainName)
	if err != nil {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "domainsecrets").
			Str("domain", domainName).
			Err(err).
			Msg("rejected invalid domain")
		return "", domain.ErrPathTraversal
	}
	return safeDomain, nil
}

// ManifestExists checks if the manifest exists for a domain.
func (s *PassStore) ManifestExists(domainName string) (bool, error) {
	manifestPath, err := s.manifestPath(domainName)
	if err != nil {
		return false, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	_, exists, err := s.passShow(ctx, manifestPath)
	if err != nil {
		return false, err
	}

	return exists, nil
}

func (s *PassStore) passInsert(ctx context.Context, path, value string) error {
	cmd := exec.CommandContext(ctx, "pass", "insert", "-m", "-f", path)
	cmd.Stdin = strings.NewReader(value)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pass insert failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (s *PassStore) passRemove(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "pass", "rm", "-f", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if passEntryMissing(string(output)) {
			return nil
		}
		return fmt.Errorf("pass rm failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (s *PassStore) passShow(ctx context.Context, path string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "pass", "show", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if passEntryMissing(string(output)) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("pass show failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), true, nil
}

func (s *PassStore) listTopLevelEntries(ctx context.Context, basePath string) ([]string, error) {
	if err := secrets.ValidatePath(basePath); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "pass", "ls", basePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if passEntryMissing(string(output)) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("pass ls failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	entries := []string{}
	for _, entry := range parsePassListOutput(basePath, string(output)) {
		if entry.depth == 1 {
			entries = append(entries, entry.name)
		}
	}

	return entries, nil
}

type passListEntry struct {
	name  string
	depth int
}

func parsePassListOutput(basePath, output string) []passListEntry {
	lines := strings.Split(output, "\n")
	entries := []passListEntry{}

	for _, line := range lines {
		line = ansiRegex.ReplaceAllString(line, "")
		if strings.TrimSpace(line) == "" {
			continue
		}

		depth := 0
		for {
			switch {
			case strings.HasPrefix(line, "│   "):
				line = strings.TrimPrefix(line, "│   ")
				depth++
			case strings.HasPrefix(line, "|   "):
				line = strings.TrimPrefix(line, "|   ")
				depth++
			case strings.HasPrefix(line, "    "):
				line = strings.TrimPrefix(line, "    ")
				depth++
			default:
				goto prefixDone
			}
		}
	prefixDone:

		switch {
		case strings.HasPrefix(line, "├── "):
			line = strings.TrimPrefix(line, "├── ")
			depth++
		case strings.HasPrefix(line, "└── "):
			line = strings.TrimPrefix(line, "└── ")
			depth++
		case strings.HasPrefix(line, "|-- "):
			line = strings.TrimPrefix(line, "|-- ")
			depth++
		case strings.HasPrefix(line, "`-- "):
			line = strings.TrimPrefix(line, "`-- ")
			depth++
		}

		name := strings.TrimSpace(line)
		if name == "" || name == basePath {
			continue
		}
		if depth == 0 {
			continue
		}

		entries = append(entries, passListEntry{name: name, depth: depth})
	}

	return entries
}

func passEntryMissing(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "not in the password store") ||
		strings.Contains(lower, "password store is empty")
}
