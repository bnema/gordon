package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestVersionCommand(t *testing.T) {
	oldVersion, oldCommit, oldDate, oldDirty := Version, Commit, BuildDate, Dirty
	SetVersionInfo("3.5.0", "8345e76", "2026-10-22T08:36:54Z", "true")
	t.Cleanup(func() { SetVersionInfo(oldVersion, oldCommit, oldDate, oldDirty) })
	oldRemote, oldToken, oldInsecure := remoteFlag, tokenFlag, insecureTLSFlag
	t.Cleanup(func() { remoteFlag, tokenFlag, insecureTLSFlag = oldRemote, oldToken, oldInsecure })
	for _, tc := range []struct {
		name, body, target string
		status             int
		mismatch, unknown  bool
	}{
		{name: "local"},
		{name: "empty explicit remote", target: "empty"},
		{name: "same", body: fmt.Sprintf(`{"version":%q,"commit":"server-commit","build_date":"server-date","dirty":"false"}`, Version), target: "flag"},
		{name: "different", body: `{"version":"other"}`, target: "short", mismatch: true},
		{name: "old", body: `{"valid":true}`, target: "flag", unknown: true},
		{name: "error", status: 500, target: "flag"},
		{name: "unreachable active remote"},
		{name: "malformed remote config"},
		{name: "named", body: `{"version":"other","commit":"abc","build_date":"date","dirty":"true"}`, target: "named", mismatch: true},
		{name: "named legacy", body: `{}`, target: "named", unknown: true},
		{name: "invalid explicit remote", target: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("PASSWORD_STORE_DIR", t.TempDir())
			t.Setenv("PATH", t.TempDir()) // Never discover the developer's pass executable.
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("GORDON_REMOTE", "")
			t.Setenv("GORDON_TOKEN", "")
			t.Setenv("GORDON_INSECURE", "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/token" {
					_, _ = fmt.Fprint(w, `{"token":"test-ephemeral","expires_in":300}`)
					return
				}
				require.Equal(t, "/admin/auth/verify", r.URL.Path)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			if tc.name == "unreachable active remote" {
				require.NoError(t, remote.SaveRemotes("", &remote.ClientConfig{Active: "offline", Remotes: map[string]remote.RemoteEntry{"offline": {URL: "http://127.0.0.1:1", Token: "test-token"}}}))
				t.Setenv("GORDON_REMOTE", "http://127.0.0.1:1")
			}
			if tc.name == "malformed remote config" {
				require.NoError(t, remote.SaveRemotes("", &remote.ClientConfig{}))
				require.NoError(t, os.WriteFile(remote.DefaultRemotesPath(), []byte("[invalid"), 0600))
			}
			cmd := NewRootCmd()
			args := []string{"version"}
			switch tc.target {
			case "flag":
				args = append(args, "--remote", server.URL)
			case "short":
				args = append(args, "-r", server.URL)
			case "named":
				require.NoError(t, remote.SaveRemotes("", &remote.ClientConfig{Remotes: map[string]remote.RemoteEntry{"production": {URL: server.URL, Token: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.c2ln"}}}))
				args = append(args, "--remote", "production")
			case "empty":
				t.Setenv("GORDON_REMOTE", server.URL)
				require.NoError(t, remote.SaveRemotes("", &remote.ClientConfig{Active: "production", Remotes: map[string]remote.RemoteEntry{"production": {URL: server.URL, Token: "test-token"}}}))
				args = append(args, "--remote=")
			case "invalid":
				args = append(args, "--remote", "missing")
			}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			err := cmd.Execute()
			if tc.target == "empty" {
				require.EqualError(t, err, "--remote requires a remote name or URL")
				return
			}
			if tc.target == "invalid" {
				require.ErrorContains(t, err, "missing")
				return
			}
			if tc.status != 0 {
				require.ErrorContains(t, err, "failed to get server version")
				return
			}
			require.NoError(t, err)
			if tc.target == "" {
				var expected bytes.Buffer
				require.NoError(t, cliWriteLine(&expected, cliRenderTitle("Gordon "+Version)))
				for _, f := range [][2]string{{"Commit:", Commit}, {"Build Date:", BuildDate}, {"Dirty:", Dirty}} {
					require.NoError(t, cliWriteLine(&expected, cliRenderMeta(f[0], f[1])))
				}
				require.Equal(t, expected.String(), out.String())
				return
			}
			require.Contains(t, out.String(), fmt.Sprintf("Client  Gordon %s  (", Version))
			displayName := server.URL
			if tc.target == "named" {
				displayName = "production"
			}
			lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
			require.Equal(t, "Client  Gordon 3.5.0  (8345e76, 2026-10-22T08:36:54Z, dirty)", string(lines[0]))
			require.True(t, bytes.HasSuffix(lines[1], []byte("  "+displayName)))
			if tc.name == "named" {
				require.Equal(t, "Server  Gordon other  (abc, date, dirty)  production", string(lines[1]))
			}
			require.Contains(t, out.String(), "Server  ")
			require.Equal(t, tc.mismatch, bytes.Contains(out.Bytes(), []byte("versions differ")))
			if tc.unknown {
				require.Contains(t, out.String(), "Server  unknown (server predates version reporting)")
			}
			if tc.name == "same" {
				require.Equal(t, fmt.Sprintf("Server  Gordon %s  (server-commit, server-date)  %s", Version, server.URL), string(lines[1]))
			}
		})
	}
}
