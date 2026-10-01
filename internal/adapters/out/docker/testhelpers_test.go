package docker

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// newRuntimeForHTTPServer builds a Runtime whose Docker API endpoint is
// the given test server, so adapter behaviour can be asserted against
// the exact HTTP requests it issues.
func newRuntimeForHTTPServer(t *testing.T, server *httptest.Server) *Runtime {
	t.Helper()

	host := strings.TrimPrefix(server.URL, "http://")
	cli, err := client.New(client.WithHost("tcp://"+host), client.WithAPIVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)

	return NewRuntimeWithClient(cli)
}
