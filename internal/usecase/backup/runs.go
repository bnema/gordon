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

// begin records job as the new running run of its target unless the
// target already has a running run. It returns that run and false then.
// Every run starts through begin, so a target never runs twice at once.
func (r *runRegistry[J]) begin(job J) (J, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta := r.meta(job)
	if prevID, ok := r.latest[meta.target]; ok {
		if prev, ok := r.runs[prevID]; ok && prev.status == domain.BackupStatusRunning {
			return prev.job, false
		}
	}
	r.order = append(r.order, meta.id)
	r.runs[meta.id] = runEntry[J]{runMeta: meta, job: job}
	r.latest[meta.target] = meta.id
	r.evictLocked()
	return job, true
}

// put records the new state of a run begun earlier. It never changes
// which run is the latest of its target, and ignores unknown runs.
func (r *runRegistry[J]) put(job J) {
	r.mu.Lock()
	defer r.mu.Unlock()
	meta := r.meta(job)
	if _, ok := r.runs[meta.id]; !ok {
		return
	}
	r.runs[meta.id] = runEntry[J]{runMeta: meta, job: job}
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
