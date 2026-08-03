// Package domainsecrets implements the DomainSecretStore adapter using pass.
package domainsecrets

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// ansiRegex matches ANSI escape sequences for stripping from pass output.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

const (
	PassDomainSecretsPath = "gordon/env"             //nolint:gosec // Not a credential, this is a pass store path.
	PassAttachmentPath    = "gordon/env/attachments" //nolint:gosec // Not a credential, this is a pass store path.
)

// PassStore implements the DomainSecretStore interface using the pass password manager.
type PassStore struct {
	mu      sync.Mutex
	timeout time.Duration
	log     zerowrap.Logger
	runPass func(context.Context, string, ...string) ([]byte, error)
}

// NewPassStore creates a new pass-based domain secret store.
func NewPassStore(log zerowrap.Logger) (*PassStore, error) {
	// Use a timeout so a stalled GPG agent or keyring does not hang startup.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer probeCancel()
	if err := exec.CommandContext(probeCtx, "pass", "version").Run(); err != nil { //nolint:gosec // binary is a constant ("pass"), no user input
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
		runPass: execPass,
	}, nil
}

// ListKeys returns the list of secret keys for a domain (not values).
func (s *PassStore) ListKeys(domainName string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listKeysLocked(domainName)
}

func (s *PassStore) listKeysLocked(domainName string) ([]string, error) {
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
	keys := []string{}
	if exists {
		for _, line := range strings.Split(content, "\n") {
			key := strings.TrimSpace(line)
			if key == "" {
				continue
			}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	// Recover keys that may exist in pass but are missing from the manifest.
	discovered, err := s.listDomainKeys(domainName)
	if err != nil {
		return nil, err
	}

	merged, changed := mergeUniqueKeys(keys, discovered)
	if changed {
		// Best-effort self-heal of stale manifest.
		writeCtx, writeCancel := context.WithTimeout(context.Background(), s.timeout)
		writeErr := s.passInsert(writeCtx, manifestPath, strings.Join(merged, "\n"))
		writeCancel()
		if writeErr != nil {
			s.log.Warn().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "domainsecrets").
				Str("domain", domainName).
				Err(writeErr).
				Msg("failed to self-heal pass manifest, continuing with recovered keys")
		}
	}

	return merged, nil
}

// GetAll returns all secrets for a domain as a key-value map.
func (s *PassStore) GetAll(domainName string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, err := s.listKeysLocked(domainName)
	if err != nil {
		return nil, err
	}

	secretsMap := make(map[string]string)
	for _, key := range keys {
		keyPath, err := s.keyPath(domainName, key)
		if err != nil {
			return nil, err
		}

		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		value, exists, err := s.passShow(ctx, keyPath)
		cancel()
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

// SetIfEmpty sets multiple secrets for a domain only when no keys already exist.
// It performs the precondition check inside the store adapter so migration
// callers cannot accidentally split the check and write into separate steps.
func (s *PassStore) SetIfEmpty(domainName string, secretsMap map[string]string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existingKeys, err := s.listKeysLocked(domainName)
	if err != nil {
		return nil, err
	}
	if len(existingKeys) > 0 {
		return nil, fmt.Errorf("%w: pass secrets already exist for %s (found %d keys)", domain.ErrSecretsAlreadyExist, domainName, len(existingKeys))
	}
	if err := s.setLocked(domainName, secretsMap); err != nil {
		return nil, err
	}
	return sortedMapKeys(secretsMap), nil
}

// Set sets or updates multiple secrets for a domain.
func (s *PassStore) Set(domainName string, secretsMap map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(domainName, secretsMap)
}

func (s *PassStore) setLocked(domainName string, secretsMap map[string]string) error {
	paths := make(map[string]string, len(secretsMap))
	for _, key := range sortedMapKeys(secretsMap) {
		if err := domain.ValidateEnvKey(key); err != nil {
			return err
		}
		path, err := s.keyPath(domainName, key)
		if err != nil {
			return err
		}
		paths[key] = path
	}
	manifestPath, err := s.manifestPath(domainName)
	if err != nil {
		return err
	}
	manifestSnapshot, err := s.snapshotPassValue(manifestPath)
	if err != nil {
		return err
	}
	existingKeys, err := s.listKeysLocked(domainName)
	if err != nil {
		return err
	}
	return s.commitSecretTransaction(existingKeys, secretsMap, paths, manifestSnapshot)
}

// Delete removes a specific secret key from a domain.
func (s *PassStore) Delete(domainName, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(domainName, key)
}

func (s *PassStore) deleteLocked(domainName, key string) error {
	keyPath, err := s.keyPath(domainName, key)
	if err != nil {
		return err
	}
	manifestPath, err := s.manifestPath(domainName)
	if err != nil {
		return err
	}
	return s.deleteSecretTransaction(key, keyPath, manifestPath, func() ([]string, error) {
		return s.listKeysLocked(domainName)
	})
}

// SetAttachmentIfEmpty sets attachment secrets only when no keys already exist.
// It performs the precondition check inside the store adapter so migration
// callers cannot accidentally split the check and write into separate steps.
func (s *PassStore) SetAttachmentIfEmpty(containerName string, secretsMap map[string]string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existingKeys, err := s.listAttachmentKeysRecover(containerName)
	if err != nil {
		return nil, err
	}
	if len(existingKeys) > 0 {
		return nil, fmt.Errorf("%w: pass secrets already exist for attachment %s (found %d keys)", domain.ErrSecretsAlreadyExist, containerName, len(existingKeys))
	}
	if err := s.setAttachmentLocked(containerName, secretsMap); err != nil {
		return nil, err
	}
	return sortedMapKeys(secretsMap), nil
}

// SetAttachment sets or updates multiple secrets for an attachment container.
func (s *PassStore) SetAttachment(containerName string, secretsMap map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setAttachmentLocked(containerName, secretsMap)
}

func (s *PassStore) setAttachmentLocked(containerName string, secretsMap map[string]string) error {
	paths := make(map[string]string, len(secretsMap))
	for _, key := range sortedMapKeys(secretsMap) {
		if err := domain.ValidateEnvKey(key); err != nil {
			return err
		}
		path, err := s.attachmentKeyPath(containerName, key)
		if err != nil {
			return err
		}
		paths[key] = path
	}
	manifestPath, err := s.attachmentManifestPath(containerName)
	if err != nil {
		return err
	}
	manifestSnapshot, err := s.snapshotPassValue(manifestPath)
	if err != nil {
		return err
	}
	existingKeys, err := s.listAttachmentKeysRecover(containerName)
	if err != nil {
		return err
	}
	return s.commitSecretTransaction(existingKeys, secretsMap, paths, manifestSnapshot)
}

// DeleteAttachment removes a specific secret key from an attachment container.
func (s *PassStore) DeleteAttachment(containerName, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteAttachmentLocked(containerName, key)
}

func (s *PassStore) deleteAttachmentLocked(containerName, key string) error {
	keyPath, err := s.attachmentKeyPath(containerName, key)
	if err != nil {
		return err
	}
	manifestPath, err := s.attachmentManifestPath(containerName)
	if err != nil {
		return err
	}
	return s.deleteSecretTransaction(key, keyPath, manifestPath, func() ([]string, error) {
		return s.listAttachmentKeys(containerName)
	})
}

// GetAllAttachment returns all secrets for an attachment container as a key-value map.
func (s *PassStore) GetAllAttachment(containerName string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, err := s.listAttachmentKeys(containerName)
	if err != nil {
		return nil, err
	}

	secretsMap := make(map[string]string)
	for _, key := range keys {
		keyPath, err := s.attachmentKeyPath(containerName, key)
		if err != nil {
			return nil, err
		}

		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		value, exists, err := s.passShow(ctx, keyPath)
		cancel()
		if err != nil {
			return nil, err
		}
		if !exists {
			s.log.Warn().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "domainsecrets").
				Str("container", containerName).
				Str("key", key).
				Msg("attachment secret listed in manifest but missing in pass")
			continue
		}
		secretsMap[key] = value
	}

	return secretsMap, nil
}

// ListAttachmentKeys finds attachment secrets for a domain from pass.
// Supports both new (collision-resistant) and legacy container naming for backwards compatibility.
func (s *PassStore) ListAttachmentKeys(domainName string) ([]out.AttachmentSecrets, error) {
	if _, err := s.domainPath(domainName); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	containers, err := s.listTopLevelEntries(ctx, PassAttachmentPath)
	if err != nil {
		return nil, err
	}

	// Try both new and legacy sanitization for backwards compatibility
	sanitizedDomain := domain.SanitizeDomainForContainer(domainName)
	sanitizedDomainLegacy := domain.SanitizeDomainForContainerLegacy(domainName)
	prefixes := []string{
		"gordon-" + sanitizedDomain + "-",       // New format (collision-resistant)
		"gordon-" + sanitizedDomainLegacy + "-", // Old format (buggy but backwards compatible)
	}

	seen := make(map[string]bool)
	var results []out.AttachmentSecrets
	for _, containerName := range containers {
		matches := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(containerName, prefix) {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}

		// Deduplicate results
		if seen[containerName] {
			continue
		}
		seen[containerName] = true

		manifestPath := fmt.Sprintf("%s/%s/.keys", PassAttachmentPath, containerName)
		if err := secrets.ValidatePath(manifestPath); err != nil {
			return nil, err
		}

		showCtx, showCancel := context.WithTimeout(context.Background(), s.timeout)
		content, exists, err := s.passShow(showCtx, manifestPath)
		showCancel()
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
	path := fmt.Sprintf("%s/%s", PassDomainSecretsPath, safeDomain)
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
	if err := domain.ValidateEnvKey(key); err != nil {
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

func (s *PassStore) listDomainKeys(domainName string) ([]string, error) {
	basePath, err := s.domainPath(domainName)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	entries, err := s.listTopLevelEntries(ctx, basePath)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == ".keys" {
			continue
		}
		if err := domain.ValidateEnvKey(entry); err != nil {
			continue
		}
		keys = append(keys, entry)
	}

	sort.Strings(keys)
	return keys, nil
}

func (s *PassStore) attachmentPath(containerName string) (string, error) {
	if err := domain.ValidateContainerName(containerName); err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/%s", PassAttachmentPath, containerName)
	if err := secrets.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *PassStore) attachmentKeyPath(containerName, key string) (string, error) {
	attachmentPath, err := s.attachmentPath(containerName)
	if err != nil {
		return "", err
	}
	if err := domain.ValidateEnvKey(key); err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/%s", attachmentPath, key)
	if err := secrets.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *PassStore) attachmentManifestPath(containerName string) (string, error) {
	attachmentPath, err := s.attachmentPath(containerName)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s/.keys", attachmentPath)
	if err := secrets.ValidatePath(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *PassStore) listAttachmentKeys(containerName string) ([]string, error) {
	manifestPath, err := s.attachmentManifestPath(containerName)
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

	sort.Strings(keys)
	return keys, nil
}

func (s *PassStore) listAttachmentKeysRecover(containerName string) ([]string, error) {
	keys, err := s.listAttachmentKeys(containerName)
	if err != nil {
		return nil, err
	}
	discovered, err := s.discoverAttachmentKeys(containerName)
	if err != nil {
		return nil, err
	}
	merged, changed := mergeUniqueKeys(keys, discovered)
	if changed {
		manifestPath, err := s.attachmentManifestPath(containerName)
		if err != nil {
			return nil, err
		}
		writeCtx, writeCancel := context.WithTimeout(context.Background(), s.timeout)
		writeErr := s.passInsert(writeCtx, manifestPath, strings.Join(merged, "\n"))
		writeCancel()
		if writeErr != nil {
			s.log.Warn().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "domainsecrets").
				Str("container", containerName).
				Err(writeErr).
				Msg("failed to self-heal attachment pass manifest, continuing with recovered keys")
		}
	}
	return merged, nil
}

func (s *PassStore) discoverAttachmentKeys(containerName string) ([]string, error) {
	basePath, err := s.attachmentPath(containerName)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	entries, err := s.listTopLevelEntries(ctx, basePath)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == ".keys" {
			continue
		}
		if err := domain.ValidateEnvKey(entry); err != nil {
			continue
		}
		keys = append(keys, entry)
	}

	sort.Strings(keys)
	return keys, nil
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

func execPass(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pass", args...) //nolint:gosec // binary is constant ("pass"); path arguments are validated by secrets path validator
	cmd.Stdin = strings.NewReader(stdin)
	return cmd.CombinedOutput()
}

func (s *PassStore) run(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	runner := s.runPass
	if runner == nil {
		runner = execPass
	}
	return runner(ctx, stdin, args...)
}

func (s *PassStore) passInsert(ctx context.Context, path, value string) error {
	_, err := s.run(ctx, value, "insert", "-m", "-f", path)
	if err != nil {
		return fmt.Errorf("pass insert failed")
	}
	return nil
}

func (s *PassStore) passRemove(ctx context.Context, path string) error {
	output, err := s.run(ctx, "", "rm", "-f", path)
	if err != nil {
		if passEntryMissing(string(output)) {
			return nil
		}
		return fmt.Errorf("pass remove failed")
	}
	return nil
}

func (s *PassStore) passShow(ctx context.Context, path string) (string, bool, error) {
	output, err := s.run(ctx, "", "show", path)
	if err != nil {
		if passEntryMissing(string(output)) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("pass show failed")
	}

	clean := ansiRegex.ReplaceAllString(string(output), "")
	clean = strings.TrimRight(clean, "\r\n")
	return clean, true, nil
}

func (s *PassStore) listTopLevelEntries(ctx context.Context, basePath string) ([]string, error) {
	if err := secrets.ValidatePath(basePath); err != nil {
		return nil, err
	}

	output, err := s.run(ctx, "", "ls", basePath)
	if err != nil {
		if passEntryMissing(string(output)) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("pass list failed")
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
	prefixLoop:
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
				break prefixLoop
			}
		}

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
	clean := ansiRegex.ReplaceAllString(output, "")
	lower := strings.ToLower(clean)
	return strings.Contains(lower, "not in the password store") ||
		strings.Contains(lower, "password store is empty")
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergeUniqueKeys(primary, secondary []string) ([]string, bool) {
	mergedSet := make(map[string]struct{}, len(primary)+len(secondary))
	for _, key := range primary {
		mergedSet[key] = struct{}{}
	}
	for _, key := range secondary {
		mergedSet[key] = struct{}{}
	}

	merged := make([]string, 0, len(mergedSet))
	for key := range mergedSet {
		merged = append(merged, key)
	}
	sort.Strings(merged)

	if len(merged) != len(primary) {
		return merged, true
	}

	for i := range primary {
		if primary[i] != merged[i] {
			return merged, true
		}
	}

	return merged, false
}

type passValueSnapshot struct {
	path   string
	value  string
	exists bool
}

func (s *PassStore) commitSecretTransaction(existingKeys []string, values, paths map[string]string, manifestSnapshot passValueSnapshot) error {
	manifestPath := manifestSnapshot.path
	keys := sortedMapKeys(values)
	var err error
	snapshots := make(map[string]passValueSnapshot, len(keys)+1)
	for _, key := range keys {
		snapshot, err := s.snapshotPassValue(paths[key])
		if err != nil {
			return err
		}
		snapshots[paths[key]] = snapshot
	}
	snapshots[manifestPath] = manifestSnapshot

	attempted := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		path := paths[key]
		attempted = append(attempted, path)
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		err = s.passInsert(ctx, path, values[key])
		cancel()
		if err != nil {
			return s.transactionFailure(attempted, snapshots)
		}
	}

	keySet := make(map[string]struct{}, len(existingKeys)+len(keys))
	for _, key := range existingKeys {
		keySet[key] = struct{}{}
	}
	for _, key := range keys {
		keySet[key] = struct{}{}
	}
	manifestKeys := make([]string, 0, len(keySet))
	for key := range keySet {
		manifestKeys = append(manifestKeys, key)
	}
	sort.Strings(manifestKeys)

	attempted = append(attempted, manifestPath)
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	err = s.passInsert(ctx, manifestPath, strings.Join(manifestKeys, "\n"))
	cancel()
	if err != nil {
		return s.transactionFailure(attempted, snapshots)
	}
	return nil
}

func (s *PassStore) deleteSecretTransaction(key, keyPath, manifestPath string, listKeys func() ([]string, error)) error {
	keySnapshot, err := s.snapshotPassValue(keyPath)
	if err != nil {
		return err
	}
	manifestSnapshot, err := s.snapshotPassValue(manifestPath)
	if err != nil {
		return err
	}
	snapshots := []passValueSnapshot{keySnapshot, manifestSnapshot}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	err = s.passRemove(ctx, keyPath)
	cancel()
	if err != nil {
		return err
	}

	keys, err := listKeys()
	if err != nil {
		return s.deleteTransactionFailure(snapshots)
	}
	updated := make([]string, 0, len(keys))
	for _, existingKey := range keys {
		if existingKey != key {
			updated = append(updated, existingKey)
		}
	}
	sort.Strings(updated)

	ctx, cancel = context.WithTimeout(context.Background(), s.timeout)
	err = s.passInsert(ctx, manifestPath, strings.Join(updated, "\n"))
	cancel()
	if err != nil {
		return s.deleteTransactionFailure(snapshots)
	}
	return nil
}

func (s *PassStore) deleteTransactionFailure(snapshots []passValueSnapshot) error {
	if s.restorePassSnapshots(snapshots) {
		return fmt.Errorf("secret delete failed and rollback failed")
	}
	return fmt.Errorf("secret delete failed")
}

func (s *PassStore) restorePassSnapshots(snapshots []passValueSnapshot) bool {
	rollbackFailed := false
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		var err error
		if snapshot.exists {
			err = s.passInsert(ctx, snapshot.path, snapshot.value)
		} else {
			err = s.passRemove(ctx, snapshot.path)
		}
		cancel()
		rollbackFailed = rollbackFailed || err != nil
	}
	return rollbackFailed
}

func (s *PassStore) snapshotPassValue(path string) (passValueSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	value, exists, err := s.passShow(ctx, path)
	if err != nil {
		return passValueSnapshot{}, err
	}
	return passValueSnapshot{path: path, value: value, exists: exists}, nil
}

func (s *PassStore) transactionFailure(attempted []string, snapshots map[string]passValueSnapshot) error {
	ordered := make([]passValueSnapshot, 0, len(attempted))
	for _, path := range attempted {
		ordered = append(ordered, snapshots[path])
	}
	if s.restorePassSnapshots(ordered) {
		return fmt.Errorf("secret transaction failed and rollback failed")
	}
	return fmt.Errorf("secret transaction failed")
}
