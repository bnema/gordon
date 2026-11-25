package backup

import (
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

// maxTrackedRuns bounds the in-memory run history of one service.
const maxTrackedRuns = 100

// runMeta is what the registry reads from one job: the target it backs
// up, its run ID, and its status.
type runMeta struct {
	target string
	id     string
	status domain.BackupJobStatus
}

// runEntry is one tracked backup run.
type runEntry[J any] struct {
	runMeta
	job J
}

// runRegistry keeps the in-memory state of backup runs: every recent run
// by ID so a caller can poll a detached run, and the latest run of each
// target so status reports show what is running or what last failed.
type runRegistry[J any] struct {
	mu     sync.Mutex
	runs   map[string]runEntry[J]
	order  []string
	latest map[string]string
	meta   func(J) runMeta
}

func newRunRegistry[J any](meta func(J) runMeta) *runRegistry[J] {
	return &runRegistry[J]{
		runs:   make(map[string]runEntry[J]),
		latest: make(map[string]string),
		meta:   meta,
	}
}

// begin records a new running run of target unless one is already
// running. It returns the running job and false in that case.
func (r *runRegistry[J]) begin(job J) (J, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prevID, ok := r.latest[r.meta(job).target]; ok {
		if prev, ok := r.runs[prevID]; ok && prev.status == domain.BackupStatusRunning {
			return prev.job, false
		}
	}
	r.storeLocked(job)
	return job, true
}

// put records the current state of one run.
func (r *runRegistry[J]) put(job J) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storeLocked(job)
}

// get returns one tracked run by ID.
func (r *runRegistry[J]) get(id string) (J, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.runs[id]
	return entry.job, ok
}

// unsettled returns the latest run of every target that is still running
// or whose latest run failed.
func (r *runRegistry[J]) unsettled() []J {
	r.mu.Lock()
	defer r.mu.Unlock()
	jobs := make([]J, 0, len(r.latest))
	for _, id := range r.latest {
		entry, ok := r.runs[id]
		if ok && (entry.status == domain.BackupStatusRunning || entry.status == domain.BackupStatusFailed) {
			jobs = append(jobs, entry.job)
		}
	}
	return jobs
}

func (r *runRegistry[J]) storeLocked(job J) {
	meta := r.meta(job)
	if _, exists := r.runs[meta.id]; !exists {
		r.order = append(r.order, meta.id)
	}
	r.runs[meta.id] = runEntry[J]{runMeta: meta, job: job}
	r.latest[meta.target] = meta.id
	r.evictLocked()
}

// evictLocked drops the oldest finished runs that are no longer the
// latest run of their target once the history exceeds its bound.
func (r *runRegistry[J]) evictLocked() {
	if len(r.runs) <= maxTrackedRuns {
		return
	}
	kept := r.order[:0]
	for _, id := range r.order {
		entry := r.runs[id]
		if len(r.runs) > maxTrackedRuns && entry.status != domain.BackupStatusRunning && r.latest[entry.target] != id {
			delete(r.runs, id)
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}
