package deployment

import "time"

// executionStartedAt is the start time the fakes of the internal tests report
// for a freshly started container: deploy and restart observe it right after
// the start.
var executionStartedAt = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
