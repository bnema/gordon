package admin

import (
	"net/http"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// retiredMutationPaths are legacy mutation endpoints removed by the v2.50
// declarative-apps cutover. Only the 410 Gone rejection dispatcher below
// remains; no old business handlers or compatibility behavior is kept.
var retiredMutationPaths = []string{
	"/deploy",
	"/restart",
	"/deploy-intent",
	"/routes",
	"/routes/by-image",
	"/attachments",
	"/attachments/by-image",
	"/attachments/orphans",
	"/attachments/prune",
	"/bootstrap",
	"/preview",
	"/previews",
	"/autoroute/allowed-domains",
}

// isRetiredMutation reports whether a request path targets a removed
// legacy mutation endpoint.
func isRetiredMutation(path string) bool {
	for _, prefix := range retiredMutationPaths {
		if path == prefix || len(path) > len(prefix) && path[:len(prefix)+1] == prefix+"/" {
			return true
		}
	}
	return false
}

// handleRetiredMutation answers removed endpoints with 410 Gone and the
// explicit endpoint-retired error. All methods on retired prefixes retire
// together: no old business handlers or compatibility behavior is kept.
func (h *Handler) handleRetiredMutation(w http.ResponseWriter, _ *http.Request, _ string) {
	h.sendJSON(w, http.StatusGone, dto.AppError{
		Error:   "endpoint-retired",
		Message: "this endpoint was removed by the v2.50 declarative-apps cutover; use /admin/apps/* instead",
		Hint:    "see gordon apps --help",
	})
}
