package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/testutils/remotemanagement"
)

// remoteManagementFamilies is deliberately an inventory rather than a method
// matrix. A management family added to NewRootCmd must decide whether it has a
// remote command-level contract and be registered here with a Cobra smoke.
var remoteManagementFamilies = func() map[string]struct{} {
	families := make(map[string]struct{}, len(remotemanagement.Families))
	for _, family := range remotemanagement.Families {
		families[family] = struct{}{}
	}
	return families
}()

func TestRemoteManagementFamilyInventory(t *testing.T) {
	root := NewRootCmd()
	actual := make([]string, 0)
	for _, command := range root.Commands() {
		if command.GroupID == groupManage {
			actual = append(actual, command.Name())
		}
	}
	sort.Strings(actual)
	registered := make([]string, 0, len(remoteManagementFamilies))
	for family := range remoteManagementFamilies {
		registered = append(registered, family)
	}
	sort.Strings(registered)
	require.Equal(t, registered, actual,
		"new management command families must be added to remoteManagementFamilies and receive a Cobra smoke case")
}

// TestRemoteControlCobraSmoke executes root commands, not remote.Client
// methods. The listener is intentionally HTTP-level: it verifies the command
// parser's request, JSON flags, and mutation payloads at the same public admin
// boundary used by the production control listener.
func TestRemoteControlCobraSmoke(t *testing.T) {
	resetControlPlaneResolutionTestState(t)
	// NewRootCmd binds persistent flags to package globals. Restore them so this
	// root-level smoke cannot redirect later CLI tests to its closed listener.
	t.Cleanup(func() {
		remoteFlag = ""
		tokenFlag = ""
		insecureTLSFlag = false
	})
	listener := newRemoteControlSmokeListener(t)
	defer listener.Close()

	tests := []struct {
		name       string
		args       []string
		wantOutput string
		wantError  string
	}{
		{"config JSON", []string{"config", "show", "--json"}, `"routes"`, ""},
		{"routes JSON", []string{"routes", "list", "--json"}, `"app.example.com"`, ""},
		{"routes add mutation", []string{"routes", "add", "new.example.com", "new:v1"}, "Route configured", ""},
		{"attachments JSON", []string{"attachments", "list", "app.example.com", "--json"}, `"sidecar:v1"`, ""},
		{"attachments add mutation", []string{"attachments", "add", "app.example.com", "redis:7"}, "Attachment added: app.example.com -> redis:7", ""},
		{"attachments remove mutation", []string{"attachments", "remove", "app.example.com", "redis:7", "--force"}, "Attachment removed: app.example.com -> redis:7", ""},
		{"secrets JSON", []string{"secrets", "list", "app.example.com", "--json"}, `"TOKEN"`, ""},
		{"deploy JSON", []string{"deploy", "app.example.com", "--json"}, `"status": "queued"`, ""},
		{"restart", []string{"restart", "app.example.com", "--with-attachments"}, "Restarted app.example.com", ""},
		{"reload", []string{"reload"}, "Configuration reloaded successfully", ""},
		{"container logs", []string{"logs", "app.example.com", "--lines", "7"}, "runtime line", ""},
		{"follow container logs", []string{"logs", "app.example.com", "--lines", "3", "--follow"}, "runtime follow line", ""},
		{"status JSON", []string{"status", "--json"}, `"container_status"`, ""},
		{"networks JSON", []string{"networks", "list", "--json"}, `"gordon-app"`, ""},
		{"autoroute JSON", []string{"autoroute", "allow", "list", "--json"}, `"*.example.com"`, ""},
		{"TLS JSON", []string{"tls", "status", "--json"}, `"acme_enabled"`, ""},
		{"traffic JSON", []string{"traffic", "status", "--json"}, `"last_reload_status"`, ""},
		// The split control listener does not own these runtime/registry services.
		// Their capability response is part of the command contract, not a skip.
		{"volumes capability", []string{"volumes", "list", "--json"}, "", "503"},
		{"images capability", []string{"images", "list", "--json"}, "", "503"},
		{"preview capability", []string{"preview", "list", "--json"}, "", "503"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := NewRootCmd()
			var out, errOut bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errOut)
			root.SetArgs(append([]string{"--remote", listener.URL, "--token", smokeLongLivedToken}, test.args...))
			err := root.Execute()
			if test.wantError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.wantError)
				return
			}
			require.NoError(t, err, errOut.String())
			require.Contains(t, out.String(), test.wantOutput)
		})
	}

	listener.state.mu.Lock()
	defer listener.state.mu.Unlock()
	require.Equal(t, "new:v1", listener.state.routes["new.example.com"])
	require.True(t, listener.state.deployed)
	require.True(t, listener.state.restartedWithAttachments)
	require.True(t, listener.state.reloaded)
	require.Equal(t, 3, listener.state.logLines)
	require.Equal(t, "app.example.com", listener.state.logDomain)
	require.True(t, listener.state.logFollow)
	require.Equal(t, []string{"redis:7"}, listener.state.addedAttachments)
	require.Equal(t, []string{"redis:7"}, listener.state.removedAttachments)
}

const smokeLongLivedToken = "remote-control-smoke-token"

type remoteControlSmokeState struct {
	mu                       sync.Mutex
	routes                   map[string]string
	deployed, reloaded       bool
	restartedWithAttachments bool
	logLines                 int
	logDomain                string
	logFollow                bool
	addedAttachments         []string
	removedAttachments       []string
}

type remoteControlSmokeListener struct {
	*httptest.Server
	state *remoteControlSmokeState
}

func newRemoteControlSmokeListener(t *testing.T) *remoteControlSmokeListener {
	t.Helper()
	state := &remoteControlSmokeState{routes: map[string]string{"app.example.com": "app:v1"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/auth/token" {
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"token": "ephemeral", "expires_in": 60})
			return
		}
		require.Equal(t, "Bearer ephemeral", request.Header.Get("Authorization"))
		path := strings.TrimPrefix(request.URL.Path, "/admin")
		state.mu.Lock()
		defer state.mu.Unlock()
		switch {
		case request.Method == http.MethodGet && path == "/config":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"server": map[string]any{}, "auto_route": map[string]any{}, "network_isolation": map[string]any{}, "routes": []map[string]string{{"domain": "app.example.com", "image": "app:v1"}}})
		case request.Method == http.MethodGet && path == "/routes":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"routes": []map[string]string{{"domain": "app.example.com", "image": "app:v1"}}})
		case request.Method == http.MethodPost && path == "/routes":
			var route struct{ Domain, Image string }
			require.NoError(t, json.NewDecoder(request.Body).Decode(&route))
			state.routes[route.Domain] = route.Image
			writeRemoteControlSmokeJSON(t, w, http.StatusCreated, map[string]string{"status": "created"})
		case request.Method == http.MethodGet && path == "/attachments/app.example.com":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"target": "app.example.com", "images": []string{"sidecar:v1"}})
		case request.Method == http.MethodPost && path == "/attachments/app.example.com":
			var attachment struct{ Image string }
			require.NoError(t, json.NewDecoder(request.Body).Decode(&attachment))
			state.addedAttachments = append(state.addedAttachments, attachment.Image)
			writeRemoteControlSmokeJSON(t, w, http.StatusCreated, map[string]string{"status": "created"})
		case request.Method == http.MethodDelete && path == "/attachments/app.example.com/redis:7":
			state.removedAttachments = append(state.removedAttachments, "redis:7")
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]string{"status": "removed"})
		case request.Method == http.MethodGet && path == "/secrets/app.example.com":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"domain": "app.example.com", "keys": []string{"TOKEN"}})
		case request.Method == http.MethodPost && path == "/deploy/app.example.com":
			state.deployed = true
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]string{"domain": "app.example.com", "status": "queued"})
		case request.Method == http.MethodPost && path == "/restart/app.example.com":
			state.restartedWithAttachments = request.URL.Query().Get("attachments") == "true"
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]string{"domain": "app.example.com"})
		case request.Method == http.MethodPost && path == "/reload":
			state.reloaded = true
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]string{"status": "ok"})
		case request.Method == http.MethodGet && path == "/logs/app.example.com":
			state.logLines = mustRemoteControlSmokeInt(t, request.URL.Query().Get("lines"))
			state.logDomain = "app.example.com"
			state.logFollow = request.URL.Query().Get("follow") == "true"
			if state.logFollow {
				w.Header().Set("Content-Type", "text/event-stream")
				_, err := fmt.Fprint(w, "data: runtime follow line\n\n")
				require.NoError(t, err)
				return
			}
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"domain": "app.example.com", "lines": []string{"runtime line"}})
		case request.Method == http.MethodGet && path == "/status":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"routes": 1, "container_status": map[string]string{"app.example.com": "running"}})
		case request.Method == http.MethodGet && path == "/networks":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"networks": []map[string]any{{"name": "gordon-app", "driver": "bridge", "containers": []string{"app"}}}})
		case request.Method == http.MethodGet && path == "/autoroute/allowed-domains":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"domains": []string{"*.example.com"}})
		case request.Method == http.MethodGet && path == "/tls/status":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"acme_enabled": false})
		case request.Method == http.MethodGet && path == "/traffic/status":
			writeRemoteControlSmokeJSON(t, w, http.StatusOK, map[string]any{"last_reload_status": "ok"})
		case request.Method == http.MethodGet && (path == "/volumes" || path == "/images" || path == "/previews"):
			writeRemoteControlSmokeJSON(t, w, http.StatusServiceUnavailable, map[string]string{"error": "capability unavailable"})
		default:
			t.Fatalf("unexpected remote Cobra request: %s %s", request.Method, request.URL.String())
		}
	}))
	return &remoteControlSmokeListener{Server: server, state: state}
}

func writeRemoteControlSmokeJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	require.NoError(t, json.NewEncoder(writer).Encode(value))
}

func mustRemoteControlSmokeInt(t *testing.T, value string) int {
	t.Helper()
	var result int
	_, err := fmt.Sscan(value, &result)
	require.NoError(t, err)
	return result
}
