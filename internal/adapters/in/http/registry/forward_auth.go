package registry

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"
)

// edgeForwardAuthHeader is private component-to-component authorization. It
// is removed before the OCI and Docker authentication handlers execute.
const edgeForwardAuthHeader = "X-Gordon-Registry-Forward-Auth"

// EdgeForwardAuth permits direct loopback probes and authenticated requests
// from the edge component. A registry has no reason to trust the component
// network CIDR: each non-loopback request must carry the distinct forwarding
// credential injected into the edge and registry private environments.
func EdgeForwardAuth(token string, _ zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get(edgeForwardAuthHeader)
			// Never allow the internal credential to reach OCI, auth, event, or
			// application code, including for loopback requests.
			r.Header.Del(edgeForwardAuthHeader)
			if registryRequestIsLoopback(r.RemoteAddr) || (token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
		})
	}
}

func registryRequestIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
