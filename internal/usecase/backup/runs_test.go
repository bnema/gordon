package backup

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type testRun struct {
	id, target string
	status     domain.BackupJobStatus
}

func newTestRegistry() *runRegistry[testRun] {
	return newRunRegistry(func(r testRun) runMeta {
		return runMeta{target: r.target, id: r.id, status: r.status}
	})
}

func running(id, target string) testRun {
	return testRun{id: id, target: target, status: domain.BackupStatusRunning}
}

func settled(run testRun, status domain.BackupJobStatus) testRun {
	run.status = status
	return run
}

// TestRunRegistry_OneRunningRunPerTarget covers begin deduplication and
// the latest-run rule when an older run settles after a newer one began.
func TestRunRegistry_OneRunningRunPerTarget(t *testing.T) {
	r := newTestRegistry()
	a := running("a", "t")
	_, started := r.begin(a)
	require.True(t, started)

	got, started := r.begin(running("b", "t"))
	assert.False(t, started, "a target never runs twice at once")
	assert.Equal(t, "a", got.id)

	r.put(settled(a, domain.BackupStatusCompleted))
	b := running("b", "t")
	_, started = r.begin(b)
	require.True(t, started, "a settled target can run again")

	r.put(settled(a, domain.BackupStatusFailed))
	_, started = r.begin(running("c", "t"))
	assert.False(t, started, "a late write of an older run never hides the running one")
	require.Len(t, r.unsettled(), 1)
	assert.Equal(t, "b", r.unsettled()[0].id)

	r.put(running("ghost", "t"))
	_, ok := r.get("ghost")
	assert.False(t, ok, "put ignores runs that never began")
}

// TestRunRegistry_EvictionKeepsRunningAndLatestRuns proves the history
// stays bounded without dropping a running run or a target's latest run.
func TestRunRegistry_EvictionKeepsRunningAndLatestRuns(t *testing.T) {
	r := newTestRegistry()
	long := running("long", "long-target")
	r.begin(long)
	for i := range maxTrackedRuns * 2 {
		run := running(fmt.Sprintf("r%d", i), "shared")
		r.begin(run)
		r.put(settled(run, domain.BackupStatusCompleted))
	}

	assert.LessOrEqual(t, len(r.runs), maxTrackedRuns+1)
	assert.Len(t, r.order, len(r.runs))
	_, ok := r.get("long")
	assert.True(t, ok, "a running run is never evicted")
	_, ok = r.get(fmt.Sprintf("r%d", maxTrackedRuns*2-1))
	assert.True(t, ok, "the latest run of a target is never evicted")
	_, ok = r.get("r0")
	assert.False(t, ok, "the oldest finished run is evicted")
}
