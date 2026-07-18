package eventoutbox

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type testPublisher struct {
	mu       sync.Mutex
	failures int
	events   []domain.ComponentEventEnvelope
}

func (p *testPublisher) PublishComponentEvent(_ context.Context, e domain.ComponentEventEnvelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures > 0 {
		p.failures--
		return errors.New("control unavailable")
	}
	p.events = append(p.events, e)
	return nil
}
func testEvent() domain.ComponentEventEnvelope {
	return domain.ComponentEventEnvelope{ID: "registry-event", Type: domain.ComponentEventTypeRegistryImagePushed, Origin: domain.ComponentRoleRegistry, Timestamp: time.Unix(1, 0), IdempotencyKey: "library/app:sha256:abc:image_pushed", AuditClassification: domain.ComponentEventAuditCritical, Payload: domain.RegistryImagePushedPayload{Repository: "library/app", Reference: "v1", Digest: "sha256:abc"}}
}
func TestOutboxPersistsOutageAndReplaysAfterRestart(t *testing.T) {
	dir := t.TempDir()
	down := &testPublisher{failures: 1}
	first, err := New(Config{Dir: dir, InitialRetry: time.Millisecond, MaxRetry: time.Millisecond}, down)
	require.NoError(t, err)
	require.NoError(t, first.PublishComponentEvent(context.Background(), testEvent()))
	require.Error(t, first.Healthy())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	info, err := entries[0].Info()
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	up := &testPublisher{}
	restarted, err := New(Config{Dir: dir, InitialRetry: time.Millisecond, MaxRetry: time.Millisecond}, up)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.Start(ctx)
	defer restarted.Close()
	require.Eventually(t, func() bool { up.mu.Lock(); defer up.mu.Unlock(); return len(up.events) == 1 }, time.Second, time.Millisecond)
	entries, err = os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)
}
func TestOutboxQuarantinesCorruptEntry(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/000.json", []byte("not json"), 0600))
	outbox, err := New(Config{Dir: dir}, &testPublisher{})
	require.NoError(t, err)
	require.Error(t, outbox.Healthy())
	_, err = os.Stat(dir + "/000.json.corrupt")
	require.NoError(t, err)
}
