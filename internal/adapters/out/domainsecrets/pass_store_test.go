package domainsecrets

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func passCmd(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "pass", args...).Run()
}

func passInsertValue(path, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pass", "insert", "-m", "-f", path)
	cmd.Stdin = strings.NewReader(value)
	_, err := cmd.CombinedOutput()
	return err
}

func passShow(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pass", "show", path).CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func requirePass(t *testing.T) {
	if err := passCmd("version"); err != nil {
		t.Skip("pass not available")
	}
	if err := passCmd("ls"); err != nil {
		t.Skip("pass store not initialized")
	}
}

func cleanupPassDomain(_ *testing.T, domainName string, keys []string) {
	safeDomain, err := domain.SanitizeDomainForEnvFile(domainName)
	if err != nil {
		return
	}

	for _, key := range keys {
		path := fmt.Sprintf("%s/%s/%s", PassDomainSecretsPath, safeDomain, key)
		_ = passCmd("rm", "-f", path)
	}

	manifestPath := fmt.Sprintf("%s/%s/.keys", PassDomainSecretsPath, safeDomain)
	_ = passCmd("rm", "-f", manifestPath)
}

func TestPassStoreMutationsWaitForStoreLock(t *testing.T) {
	store := &PassStore{
		timeout: time.Second,
		log:     testLogger(),
		runPass: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, nil
		},
	}
	mutations := []struct {
		name string
		run  func() error
	}{
		{"set", func() error { return store.Set("app.example.test", map[string]string{}) }},
		{"delete", func() error { return store.Delete("app.example.test", "TOKEN") }},
		{"set attachment", func() error { return store.SetAttachment("app-db", map[string]string{}) }},
		{"delete attachment", func() error { return store.DeleteAttachment("app-db", "TOKEN") }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			store.mu.Lock()
			done := make(chan struct{})
			go func() { _ = mutation.run(); close(done) }()
			select {
			case <-done:
				store.mu.Unlock()
				t.Fatal("mutation bypassed store lock")
			case <-time.After(20 * time.Millisecond):
			}
			store.mu.Unlock()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("mutation remained blocked after lock release")
			}
		})
	}
}

func TestPassStoreDeleteRollbackIsSerializedAgainstConcurrentSet(t *testing.T) {
	safeDomain, err := domain.SanitizeDomainForEnvFile("app.example.test")
	require.NoError(t, err)
	basePath := PassDomainSecretsPath + "/" + safeDomain
	entries := map[string]string{
		basePath + "/TOKEN": "original",
		basePath + "/.keys": "TOKEN",
	}
	manifestFailureReached := make(chan struct{})
	releaseFailure := make(chan struct{})
	failed := false
	store := &PassStore{timeout: time.Second, log: testLogger()}
	store.runPass = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		path := args[len(args)-1]
		switch args[0] {
		case "show":
			value, ok := entries[path]
			if !ok {
				return []byte("not in the password store"), errors.New("missing")
			}
			return []byte(value), nil
		case "ls":
			return []byte(path + "\n├── .keys\n└── TOKEN\n"), nil
		case "rm":
			delete(entries, path)
			return nil, nil
		case "insert":
			if strings.HasSuffix(path, "/.keys") && !failed {
				failed = true
				close(manifestFailureReached)
				<-releaseFailure
				return nil, errors.New("forced manifest failure")
			}
			entries[path] = stdin
			return nil, nil
		default:
			return nil, errors.New("unexpected pass command")
		}
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- store.Delete("app.example.test", "TOKEN") }()
	<-manifestFailureReached
	setDone := make(chan error, 1)
	go func() { setDone <- store.Set("app.example.test", map[string]string{"NEXT": "next"}) }()
	select {
	case <-setDone:
		t.Fatal("concurrent set entered while delete rollback was pending")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFailure)
	require.Error(t, <-deleteDone)
	require.NoError(t, <-setDone)
	assert.Equal(t, "original", entries[basePath+"/TOKEN"])
	assert.Equal(t, "next", entries[basePath+"/NEXT"])
}

func TestPassStoreDeleteRollsBackEntryAndManifestAfterPostRemoveListFailure(t *testing.T) {
	safeDomain, err := domain.SanitizeDomainForEnvFile("app.example.test")
	require.NoError(t, err)
	tests := []struct {
		name         string
		base         string
		deleteSecret func(*PassStore) error
	}{
		{
			name: "domain",
			base: PassDomainSecretsPath + "/" + safeDomain,
			deleteSecret: func(store *PassStore) error {
				return store.Delete("app.example.test", "TOKEN")
			},
		},
		{
			name: "attachment",
			base: PassAttachmentPath + "/app-db",
			deleteSecret: func(store *PassStore) error {
				return store.DeleteAttachment("app-db", "TOKEN")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := map[string]string{test.base + "/TOKEN": "original", test.base + "/.keys": "TOKEN"}
			removed := false
			store := &PassStore{timeout: time.Second, log: testLogger()}
			store.runPass = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
				path := args[len(args)-1]
				switch args[0] {
				case "show":
					if removed && test.name == "attachment" && path == test.base+"/.keys" {
						return nil, errors.New("forced post-remove attachment list failure")
					}
					value, ok := entries[path]
					if !ok {
						return []byte("not in the password store"), errors.New("missing")
					}
					return []byte(value), nil
				case "ls":
					if removed && test.name == "domain" {
						return nil, errors.New("forced post-remove domain list failure")
					}
					return []byte(path + "\n├── .keys\n└── TOKEN\n"), nil
				case "rm":
					delete(entries, path)
					if path == test.base+"/TOKEN" {
						removed = true
					}
					return nil, nil
				case "insert":
					entries[path] = stdin
					return nil, nil
				default:
					return nil, errors.New("unexpected command")
				}
			}

			err := test.deleteSecret(store)
			require.EqualError(t, err, "secret delete failed")
			assert.Equal(t, "original", entries[test.base+"/TOKEN"])
			assert.Equal(t, "TOKEN", entries[test.base+"/.keys"])
		})
	}
}

func TestPassStoreDeleteReturnsGenericErrorWhenPostRemoveRollbackFails(t *testing.T) {
	safeDomain, err := domain.SanitizeDomainForEnvFile("app.example.test")
	require.NoError(t, err)
	tests := []struct {
		name         string
		base         string
		deleteSecret func(*PassStore) error
	}{
		{
			name: "domain",
			base: PassDomainSecretsPath + "/" + safeDomain,
			deleteSecret: func(store *PassStore) error {
				return store.Delete("app.example.test", "TOKEN")
			},
		},
		{
			name: "attachment",
			base: PassAttachmentPath + "/app-db",
			deleteSecret: func(store *PassStore) error {
				return store.DeleteAttachment("app-db", "TOKEN")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := map[string]string{test.base + "/TOKEN": "original", test.base + "/.keys": "TOKEN"}
			removed := false
			rollbackAttempts := 0
			store := &PassStore{timeout: time.Second, log: testLogger()}
			store.runPass = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
				path := args[len(args)-1]
				switch args[0] {
				case "show":
					if removed && test.name == "attachment" && path == test.base+"/.keys" {
						return nil, errors.New("sensitive list failure")
					}
					value, ok := entries[path]
					if !ok {
						return []byte("not in the password store"), errors.New("missing")
					}
					return []byte(value), nil
				case "ls":
					if removed && test.name == "domain" {
						return nil, errors.New("sensitive list failure")
					}
					return []byte(path + "\n├── .keys\n└── TOKEN\n"), nil
				case "rm":
					delete(entries, path)
					if path == test.base+"/TOKEN" {
						removed = true
					}
					return nil, nil
				case "insert":
					rollbackAttempts++
					if path == test.base+"/TOKEN" {
						return nil, errors.New("sensitive rollback failure")
					}
					entries[path] = stdin
					return nil, nil
				default:
					return nil, errors.New("unexpected command")
				}
			}

			err := test.deleteSecret(store)
			require.EqualError(t, err, "secret delete failed and rollback failed")
			assert.Equal(t, 2, rollbackAttempts, "both snapshots must be restored even after one rollback step fails")
			assert.Equal(t, "TOKEN", entries[test.base+"/.keys"])
			assert.NotContains(t, err.Error(), "sensitive")
		})
	}
}

func TestPassStoreSetRollsBackOverwritesAndManifestOnFailure(t *testing.T) {
	safeDomain, err := domain.SanitizeDomainForEnvFile("app.example.test")
	require.NoError(t, err)
	base := PassDomainSecretsPath + "/" + safeDomain
	entries := map[string]string{
		base + "/A":     "old-a",
		base + "/B":     "old-b",
		base + "/.keys": "A\nB",
	}
	manifestWrites := 0
	store := &PassStore{timeout: time.Second, log: testLogger()}
	store.runPass = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		path := args[len(args)-1]
		switch args[0] {
		case "show":
			value, ok := entries[path]
			if !ok {
				return []byte("not in the password store"), errors.New("missing")
			}
			return []byte(value), nil
		case "ls":
			return []byte(base + "\n├── .keys\n├── A\n└── B\n"), nil
		case "insert":
			if path == base+"/.keys" {
				manifestWrites++
				if manifestWrites == 1 {
					return nil, errors.New("private backend detail")
				}
			}
			entries[path] = stdin
			return nil, nil
		case "rm":
			delete(entries, path)
			return nil, nil
		default:
			return nil, errors.New("unexpected command")
		}
	}

	err = store.Set("app.example.test", map[string]string{"A": "new-a", "B": "new-b", "C": "new-c"})
	require.Error(t, err)
	assert.Equal(t, "old-a", entries[base+"/A"])
	assert.Equal(t, "old-b", entries[base+"/B"])
	assert.NotContains(t, entries, base+"/C")
	assert.Equal(t, "A\nB", entries[base+"/.keys"])
	assert.NotContains(t, err.Error(), "private backend detail")
	assert.NotContains(t, err.Error(), "new-a")
}

func TestPassStoreSetReturnsGenericErrorWhenRollbackFails(t *testing.T) {
	safeDomain, err := domain.SanitizeDomainForEnvFile("app.example.test")
	require.NoError(t, err)
	base := PassDomainSecretsPath + "/" + safeDomain
	entries := map[string]string{base + "/A": "old", base + "/.keys": "A"}
	insertCalls := 0
	store := &PassStore{timeout: time.Second, log: testLogger()}
	store.runPass = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		path := args[len(args)-1]
		switch args[0] {
		case "show":
			value, ok := entries[path]
			if !ok {
				return []byte("not in the password store"), errors.New("missing")
			}
			return []byte(value), nil
		case "ls":
			return []byte(base + "\n├── .keys\n└── A\n"), nil
		case "insert":
			insertCalls++
			if insertCalls >= 2 {
				return nil, errors.New("sensitive failure")
			}
			entries[path] = stdin
			return nil, nil
		case "rm":
			return nil, errors.New("sensitive rollback failure")
		}
		return nil, errors.New("unexpected")
	}

	err = store.Set("app.example.test", map[string]string{"A": "new"})
	require.EqualError(t, err, "secret transaction failed and rollback failed")
}

func TestPassStoreGetAllHoldsLockForCompleteRead(t *testing.T) {
	store := &PassStore{timeout: time.Second, log: testLogger()}
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	store.runPass = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		path := args[len(args)-1]
		if args[0] == "ls" {
			return []byte(path + "\n└── A\n"), nil
		}
		if strings.HasSuffix(path, "/.keys") {
			return []byte("A"), nil
		}
		close(readStarted)
		<-releaseRead
		return []byte("value"), nil
	}
	readDone := make(chan error, 1)
	go func() { _, err := store.GetAll("app.example.test"); readDone <- err }()
	<-readStarted
	writeDone := make(chan error, 1)
	go func() { writeDone <- store.Set("app.example.test", map[string]string{}) }()
	select {
	case <-writeDone:
		t.Fatal("writer entered during multi-item read")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseRead)
	require.NoError(t, <-readDone)
	require.NoError(t, <-writeDone)
}

func TestPassStore_SetGetDelete(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	domainName := fmt.Sprintf("test.%d.example.com", time.Now().UnixNano())
	keys := []string{"API_KEY", "DB_PASSWORD"}
	defer cleanupPassDomain(t, domainName, keys)

	secretsMap := map[string]string{
		"API_KEY":     "alpha",
		"DB_PASSWORD": "bravo",
	}

	err = store.Set(domainName, secretsMap)
	require.NoError(t, err)

	keysList, err := store.ListKeys(domainName)
	require.NoError(t, err)
	assert.Len(t, keysList, 2)
	assert.ElementsMatch(t, keys, keysList)

	values, err := store.GetAll(domainName)
	require.NoError(t, err)
	assert.Equal(t, "alpha", values["API_KEY"])
	assert.Equal(t, "bravo", values["DB_PASSWORD"])

	err = store.Delete(domainName, "DB_PASSWORD")
	require.NoError(t, err)

	keysList, err = store.ListKeys(domainName)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"API_KEY"}, keysList)
}

func TestPassStore_SetGetAttachment(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	containerName := fmt.Sprintf("gitea-postgres-%d", time.Now().UnixNano())
	keys := []string{"POSTGRES_USER", "POSTGRES_PASSWORD"}
	defer CleanupPassAttachment(t, containerName, keys)

	secretsMap := map[string]string{
		"POSTGRES_USER":     "gitea",
		"POSTGRES_PASSWORD": "secret123",
	}

	err = store.SetAttachment(containerName, secretsMap)
	require.NoError(t, err)

	values, err := store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.Equal(t, "gitea", values["POSTGRES_USER"])
	assert.Equal(t, "secret123", values["POSTGRES_PASSWORD"])
}

func TestPassStore_ListAttachmentKeys_AfterSet(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	containerName := fmt.Sprintf("redis-cache-%d", time.Now().UnixNano())
	keys := []string{"REDIS_PASSWORD"}
	defer CleanupPassAttachment(t, containerName, keys)

	secretsMap := map[string]string{
		"REDIS_PASSWORD": "redis123",
	}

	err = store.SetAttachment(containerName, secretsMap)
	require.NoError(t, err)

	values, err := store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.Len(t, values, 1)
	assert.Equal(t, "redis123", values["REDIS_PASSWORD"])
}

func TestPassStore_DeleteAttachment(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	containerName := fmt.Sprintf("gitea-postgres-%d", time.Now().UnixNano())
	keys := []string{"POSTGRES_USER", "POSTGRES_PASSWORD"}
	defer CleanupPassAttachment(t, containerName, keys)

	// Set 2 secrets
	secretsMap := map[string]string{
		"POSTGRES_USER":     "gitea",
		"POSTGRES_PASSWORD": "secret123",
	}
	err = store.SetAttachment(containerName, secretsMap)
	require.NoError(t, err)

	// Delete 1
	err = store.DeleteAttachment(containerName, "POSTGRES_PASSWORD")
	require.NoError(t, err)

	// Verify remaining via GetAllAttachment
	values, err := store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.Len(t, values, 1)
	assert.Equal(t, "gitea", values["POSTGRES_USER"])
	_, exists := values["POSTGRES_PASSWORD"]
	assert.False(t, exists)
}

func TestPassStore_Delete_Idempotent(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	domainName := fmt.Sprintf("idempotent.%d.example.com", time.Now().UnixNano())
	keys := []string{"API_KEY"}
	defer cleanupPassDomain(t, domainName, keys)

	err = store.Set(domainName, map[string]string{"API_KEY": "alpha"})
	require.NoError(t, err)

	err = store.Delete(domainName, "API_KEY")
	require.NoError(t, err)

	// Second delete of an already-removed key must be a no-op.
	err = store.Delete(domainName, "API_KEY")
	require.NoError(t, err)

	values, err := store.GetAll(domainName)
	require.NoError(t, err)
	assert.NotContains(t, values, "API_KEY")
}

func TestPassStore_DeleteAttachment_Idempotent(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	containerName := fmt.Sprintf("idempotent-attachment-%d", time.Now().UnixNano())
	keys := []string{"POSTGRES_PASSWORD"}
	defer CleanupPassAttachment(t, containerName, keys)

	err = store.SetAttachment(containerName, map[string]string{"POSTGRES_PASSWORD": "secret123"})
	require.NoError(t, err)

	err = store.DeleteAttachment(containerName, "POSTGRES_PASSWORD")
	require.NoError(t, err)

	// Second delete of an already-removed key must be a no-op.
	err = store.DeleteAttachment(containerName, "POSTGRES_PASSWORD")
	require.NoError(t, err)

	values, err := store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.NotContains(t, values, "POSTGRES_PASSWORD")
}

func TestPassStore_SetAttachment_OverwriteValue(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	containerName := fmt.Sprintf("attachment-overwrite-%d", time.Now().UnixNano())
	keys := []string{"REDIS_PASSWORD"}
	defer CleanupPassAttachment(t, containerName, keys)

	err = store.SetAttachment(containerName, map[string]string{"REDIS_PASSWORD": "first"})
	require.NoError(t, err)

	// Re-setting same key with a different value should deterministically overwrite.
	err = store.SetAttachment(containerName, map[string]string{"REDIS_PASSWORD": "second"})
	require.NoError(t, err)

	values, err := store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.Equal(t, "second", values["REDIS_PASSWORD"])
}

func TestPassStore_ListKeys_RecoversOrphanedEntries(t *testing.T) {
	requirePass(t)

	store, err := NewPassStore(testLogger())
	require.NoError(t, err)

	domainName := fmt.Sprintf("orphan.%d.example.com", time.Now().UnixNano())
	keys := []string{"EXISTING", "ORIGIN"}
	defer cleanupPassDomain(t, domainName, keys)

	err = store.Set(domainName, map[string]string{"EXISTING": "present"})
	require.NoError(t, err)

	safeDomain, err := domain.SanitizeDomainForEnvFile(domainName)
	require.NoError(t, err)

	orphanPath := fmt.Sprintf("%s/%s/ORIGIN", PassDomainSecretsPath, safeDomain)
	err = passInsertValue(orphanPath, "https://example.com")
	require.NoError(t, err)

	manifestPath := fmt.Sprintf("%s/%s/.keys", PassDomainSecretsPath, safeDomain)
	err = passInsertValue(manifestPath, "EXISTING\n")
	require.NoError(t, err)

	listed, err := store.ListKeys(domainName)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"EXISTING", "ORIGIN"}, listed)

	values, err := store.GetAll(domainName)
	require.NoError(t, err)
	assert.Equal(t, "present", values["EXISTING"])
	assert.Equal(t, "https://example.com", values["ORIGIN"])

	manifest, err := passShow(manifestPath)
	require.NoError(t, err)
	assert.Contains(t, manifest, "EXISTING")
	assert.Contains(t, manifest, "ORIGIN")
}

func TestParsePassListOutput(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		output   string
		want     []passListEntry
	}{
		{
			name:     "simple tree",
			basePath: "domain",
			output:   "domain\n├── key1\n└── key2",
			want:     []passListEntry{{name: "key1", depth: 1}, {name: "key2", depth: 1}},
		},
		{
			name:     "nested tree",
			basePath: "domain",
			output:   "domain\n│   ├── subkey1\n│   └── subkey2\n└── key1",
			want:     []passListEntry{{name: "subkey1", depth: 2}, {name: "subkey2", depth: 2}, {name: "key1", depth: 1}},
		},
		{
			name:     "empty output",
			basePath: "domain",
			output:   "",
			want:     []passListEntry{},
		},
		{
			name:     "ASCII fallback chars",
			basePath: "domain",
			output:   "domain\n|   |-- subkey1\n`-- key1",
			want:     []passListEntry{{name: "subkey1", depth: 2}, {name: "key1", depth: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePassListOutput(tt.basePath, tt.output)
			assert.Equal(t, tt.want, got)
		})
	}
}
