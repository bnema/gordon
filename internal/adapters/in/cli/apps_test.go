package cli

// Tests for the unreachable v2.50 app CLI surface (apps.go). The commands
// are not registered in root.go until cutover; these tests exercise the
// run functions directly against a scripted AppControlPlane fake: JSON
// parity (text and --json carry equivalent semantics), no secret values in
// output, plan-contract errors, and outcome-unknown guidance.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// fakeAppPlane scripts AppControlPlane responses for CLI tests.
type fakeAppPlane struct {
	applyResp   *dto.AppApplyResponse
	applyErr    error
	listResp    []dto.AppSummaryDTO
	showResp    *dto.AppShowResponse
	diffResp    *dto.AppDiffResponse
	deployResp  *dto.AppDeployResponse
	deployKey   string
	deployErr   error
	lifecycleFn func(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	setErr      error
	setGot      dto.AppSecretSetRequest
	setGotApp   string
	deleteErr   error
}

func (f *fakeAppPlane) ApplyApp(_ context.Context, _ dto.AppApplyRequest) (*dto.AppApplyResponse, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	return f.applyResp, nil
}

func (f *fakeAppPlane) ListApps(_ context.Context) ([]dto.AppSummaryDTO, error) {
	return f.listResp, nil
}

func (f *fakeAppPlane) ShowApp(_ context.Context, _ string) (*dto.AppShowResponse, error) {
	return f.showResp, nil
}

func (f *fakeAppPlane) DiffApp(_ context.Context, _ string) (*dto.AppDiffResponse, error) {
	return f.diffResp, nil
}

func (f *fakeAppPlane) DeployApp(_ context.Context, _ string, _ dto.AppDeployRequest) (*dto.AppDeployResponse, string, error) {
	return f.deployResp, f.deployKey, f.deployErr
}

func (f *fakeAppPlane) StopApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return f.lifecycleFn(ctx, app)
}

func (f *fakeAppPlane) StartApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return f.lifecycleFn(ctx, app)
}

func (f *fakeAppPlane) RestartApp(_ context.Context, _ string, _ string) (*dto.AppDeployResponse, string, error) {
	return f.deployResp, f.deployKey, f.deployErr
}

func (f *fakeAppPlane) RemoveApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return f.lifecycleFn(ctx, app)
}

func (f *fakeAppPlane) OperationByKey(_ context.Context, _, _ string) (*dto.AppDeployResponse, error) {
	return f.deployResp, f.deployErr
}

func (f *fakeAppPlane) SetAppSecrets(_ context.Context, app string, req dto.AppSecretSetRequest) error {
	f.setGotApp = app
	f.setGot = req
	return f.setErr
}

func (f *fakeAppPlane) DeleteAppSecret(_ context.Context, _ string, _ dto.AppSecretDeleteRequest) error {
	return f.deleteErr
}

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.toml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestRunAppsApply_RejectsDryRunWithDeploy(t *testing.T) {
	plane := &fakeAppPlane{}
	err := runAppsApply(context.Background(), plane, strings.NewReader(""), &bytes.Buffer{}, "x.toml", true, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--dry-run")
	assert.Contains(t, err.Error(), "--deploy")
}

func TestRunAppsApply_RequiresFile(t *testing.T) {
	plane := &fakeAppPlane{}
	err := runAppsApply(context.Background(), plane, strings.NewReader(""), &bytes.Buffer{}, "", false, false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--file")
}

func TestRunAppsApply_JSONParity(t *testing.T) {
	want := &dto.AppApplyResponse{
		App: "blog", FormerRevision: "rev-a", ResultingRevision: "rev-b",
		Pending: true, Intent: "apply-123",
		Diff: dto.AppDiffSection{Added: []string{"service.web"}},
	}
	plane := &fakeAppPlane{applyResp: want}
	var out bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out,
		writeManifest(t, "[app]\nname = \"blog\"\n"), false, false, true))
	var got dto.AppApplyResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, *want, got)
}

func TestRunAppsApply_ChainsAcceptedRevision(t *testing.T) {
	plane := &fakeAppPlane{
		applyResp:  &dto.AppApplyResponse{App: "blog", ResultingRevision: "rev-b", Pending: true, Intent: "apply-1"},
		deployResp: &dto.AppDeployResponse{Op: "op-1", App: "blog", Revision: "rev-b", Outcome: "success"},
	}
	var out bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out,
		writeManifest(t, "x"), false, true, false))
	assert.Contains(t, out.String(), "rev-b")
}

func TestRunAppsApply_ChainedJSONIsOneDocument(t *testing.T) {
	plane := &fakeAppPlane{
		applyResp:  &dto.AppApplyResponse{App: "blog", ResultingRevision: "rev-b", Pending: true, Intent: "apply-1"},
		deployResp: &dto.AppDeployResponse{Op: "op-1", App: "blog", Revision: "rev-b", Outcome: "success"},
	}
	var out bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out,
		writeManifest(t, "x"), false, true, true))
	var got struct {
		Apply  dto.AppApplyResponse  `json:"apply"`
		Deploy dto.AppDeployResponse `json:"deploy"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "apply-1", got.Apply.Intent)
	assert.Equal(t, "op-1", got.Deploy.Op)
}

func TestRunAppsList_JSONParityAndSorting(t *testing.T) {
	plane := &fakeAppPlane{listResp: []dto.AppSummaryDTO{
		{App: "zeta", Converged: true},
		{App: "alpha", Stopped: true},
	}}
	var out bytes.Buffer
	require.NoError(t, runAppsList(context.Background(), plane, &out, true))
	var got []dto.AppSummaryDTO
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Len(t, got, 2)

	out.Reset()
	require.NoError(t, runAppsList(context.Background(), plane, &out, false))
	text := out.String()
	assert.Less(t, strings.Index(text, "alpha"), strings.Index(text, "zeta"), "text lists apps sorted")
	assert.Contains(t, text, "stopped")
}

func TestRunAppsList_EmptyJSONIsArray(t *testing.T) {
	plane := &fakeAppPlane{}
	var out bytes.Buffer
	require.NoError(t, runAppsList(context.Background(), plane, &out, true))
	assert.JSONEq(t, `[]`, strings.TrimSpace(out.String()))
}

func TestRunAppsShow_JSONParity(t *testing.T) {
	want := &dto.AppShowResponse{
		App:     "blog",
		Desired: dto.AppDesiredDTO{Revision: "rev-b", Status: "pending"},
		Active: dto.AppActiveDTO{Converged: false, Services: map[string]dto.AppActiveServiceDTO{
			"web": {EffectiveRevision: "rev-a", Container: "ctr-a", RestartUnsafe: true},
		}},
		Intent: dto.AppIntentDTO{Stopped: true},
		LastOp: &dto.AppLastOpDTO{Op: "op-9", Outcome: "failed"},
	}
	plane := &fakeAppPlane{showResp: want}
	var out bytes.Buffer
	require.NoError(t, runAppsShow(context.Background(), plane, "blog", &out, true))
	var got dto.AppShowResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, *want, got)

	out.Reset()
	require.NoError(t, runAppsShow(context.Background(), plane, "blog", &out, false))
	assert.Contains(t, out.String(), "restart_unsafe", "text shows WHY recovery is blocked")
}

func TestRunAppsSecretsSet_NeverEchoesValues(t *testing.T) {
	plane := &fakeAppPlane{}
	var out bytes.Buffer
	require.NoError(t, runAppsSecretsSet(context.Background(), plane, strings.NewReader(""),
		&out, "blog", []string{"password=s3cr3t-hunter2", "token=abc"}, "web", false, false))
	text := out.String()
	assert.NotContains(t, text, "s3cr3t-hunter2")
	assert.NotContains(t, text, "abc")
	assert.Contains(t, text, "password")
	assert.Contains(t, text, "token")
	assert.Equal(t, "blog", plane.setGotApp)
	assert.Equal(t, "web", plane.setGot.Service)
	assert.Equal(t, map[string]string{"password": "s3cr3t-hunter2", "token": "abc"}, plane.setGot.Secrets)
}

func TestRunAppsSecretsSet_RequiresService(t *testing.T) {
	plane := &fakeAppPlane{}
	err := runAppsSecretsSet(context.Background(), plane, strings.NewReader(""),
		&bytes.Buffer{}, "blog", []string{"k=v"}, "", false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--service")
}

func TestRunAppsSecretsSet_StdinAndValidation(t *testing.T) {
	plane := &fakeAppPlane{}
	var out bytes.Buffer
	stdin := strings.NewReader("from_stdin=stdin-value\n\nempty_val=\n")
	require.NoError(t, runAppsSecretsSet(context.Background(), plane, stdin,
		&out, "blog", []string{"from_flag=flag-value"}, "web", true, false))
	assert.Equal(t, map[string]string{
		"from_stdin": "stdin-value",
		"empty_val":  "",
		"from_flag":  "flag-value",
	}, plane.setGot.Secrets)

	err := runAppsSecretsSet(context.Background(), plane, strings.NewReader(""),
		&bytes.Buffer{}, "blog", []string{"no-equals-here"}, "web", false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KEY=VALUE")
}

func TestRunAppDeploy_OutcomeUnknownMentionsKey(t *testing.T) {
	plane := &fakeAppPlane{
		deployKey: "key-abc",
		deployErr: &remote.OutcomeUnknownError{Method: "POST", Path: "/apps/blog/deploy", Err: errors.New("boom")},
	}
	err := runAppDeploy(context.Background(), plane, "blog", "", "", &bytes.Buffer{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outcome-unknown")
	assert.Contains(t, err.Error(), "key-abc")
	assert.Contains(t, err.Error(), "by-key")
}

func TestRunAppDeploy_ConflictRendersJournal(t *testing.T) {
	plane := &fakeAppPlane{
		deployKey: "key-9",
		deployErr: &remote.AppOpConflictError{
			StatusCode: http.StatusConflict, Status: "409 Conflict",
			Response: dto.AppDeployResponse{
				Op: "op-9", App: "blog", Revision: "rev-b", Outcome: "failed",
				Services: map[string]dto.AppServiceResultDTO{
					"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "nope"},
				},
			},
		},
	}
	var out bytes.Buffer
	err := runAppDeploy(context.Background(), plane, "blog", "", "", &out, false)
	require.Error(t, err, "conflict must retain failure semantics")
	text := out.String()
	assert.Contains(t, text, "web:")
	assert.Contains(t, text, "nope")
	assert.Contains(t, err.Error(), "failed")
	assert.Contains(t, err.Error(), "op-9")
	assert.Contains(t, err.Error(), "key-9")
	assert.Contains(t, err.Error(), "by-key")
}

func TestRunAppDeploy_ConflictJSONRendersJournal(t *testing.T) {
	plane := &fakeAppPlane{
		deployKey: "key-9",
		deployErr: &remote.AppOpConflictError{
			StatusCode: http.StatusConflict, Status: "409 Conflict",
			Response: dto.AppDeployResponse{
				Op: "op-9", App: "blog", Revision: "rev-b", Outcome: "failed",
				Services: map[string]dto.AppServiceResultDTO{
					"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "nope"},
				},
			},
		},
	}
	var out bytes.Buffer
	err := runAppDeploy(context.Background(), plane, "blog", "", "", &out, true)
	require.Error(t, err)
	var got dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "op-9", got.Op)
	assert.Equal(t, "failed", got.Services["web"].Result)
}

func TestRunAppLifecycle_ConflictRendersJournal(t *testing.T) {
	plane := &fakeAppPlane{
		lifecycleFn: func(_ context.Context, _ string) (*dto.AppDeployResponse, string, error) {
			return nil, "key-7", &remote.AppOpConflictError{
				StatusCode: http.StatusConflict, Status: "409 Conflict",
				Response: dto.AppDeployResponse{
					Op: "op-7", App: "blog", Outcome: "partial",
					Services: map[string]dto.AppServiceResultDTO{
						"web": {Result: "failed", EffectiveRevision: "rev-b", Error: "busy"},
					},
				},
			}
		},
	}
	var out bytes.Buffer
	err := runAppLifecycle(context.Background(), plane.StopApp, "stop", "blog", &out, false)
	require.Error(t, err)
	assert.Contains(t, out.String(), "busy")
	assert.Contains(t, err.Error(), "key-7")
	assert.Contains(t, err.Error(), "by-key")
}

func TestRenderAppDeployResponse_SortedServices(t *testing.T) {
	resp := &dto.AppDeployResponse{
		Op: "op-1", App: "blog", Outcome: "partial",
		Services: map[string]dto.AppServiceResultDTO{
			"web": {Result: "deployed", EffectiveRevision: "rev-b"},
			"db":  {Result: "failed", EffectiveRevision: "rev-a", Error: "nope", RestartUnsafe: true},
		},
		CleanupWarnings: []dto.AppCleanupWarningDTO{{Service: "web", Leftover: "ctr-old", Detail: "retire failed"}},
		Effective:       &dto.AppEffectiveDTO{Services: map[string]string{"web": "rev-b", "db": "rev-a"}},
		Retained:        &dto.AppRetainedDTO{Volumes: []string{"blog-db"}},
	}
	var out bytes.Buffer
	require.NoError(t, renderAppDeployResponse(&out, resp))
	text := out.String()
	assert.Less(t, strings.Index(text, "db:"), strings.Index(text, "web:"))
	assert.Contains(t, text, "restart_unsafe")
	assert.Contains(t, text, "cleanup:")
	assert.Contains(t, text, "blog-db")
}

func TestAppLogRef_Selection(t *testing.T) {
	multi := &dto.AppShowResponse{App: "blog", Active: dto.AppActiveDTO{Services: map[string]dto.AppActiveServiceDTO{
		"web": {Container: "ctr-web"},
		"db":  {Container: "ctr-db"},
	}}}
	_, err := appLogRef(multi, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--service")

	ref, err := appLogRef(multi, "db")
	require.NoError(t, err)
	assert.Equal(t, "blog/db", ref)

	_, err = appLogRef(multi, "missing")
	require.Error(t, err)

	single := &dto.AppShowResponse{App: "solo", Active: dto.AppActiveDTO{Services: map[string]dto.AppActiveServiceDTO{
		"web": {Container: "ctr-solo"},
	}}}
	ref, err = appLogRef(single, "")
	require.NoError(t, err)
	assert.Equal(t, "solo/web", ref)

	empty := &dto.AppShowResponse{App: "new", Active: dto.AppActiveDTO{Services: map[string]dto.AppActiveServiceDTO{
		"web": {},
	}}}
	_, err = appLogRef(empty, "web")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no recorded container")
}

func TestResolveAppControlPlane_FailsWithoutDaemon(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	origRemote, origToken, origInsecure := remoteFlag, tokenFlag, insecureTLSFlag
	t.Cleanup(func() { remoteFlag, tokenFlag, insecureTLSFlag = origRemote, origToken, origInsecure })
	remoteFlag, tokenFlag, insecureTLSFlag = "", "", false

	restore := newLocalAppClient
	newLocalAppClient = func() (*remote.Client, error) { return nil, remote.ErrDaemonUnavailable }
	t.Cleanup(func() { newLocalAppClient = restore })

	_, err := resolveAppControlPlane()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daemon-unavailable")
}

// TestAppCommandSurface pins the frozen CLI surface (05-api-cli.md §5):
// every command exists with its frozen Use string and flags. The commands
// stay unregistered until cutover; this test is what keeps them referenced.
func TestAppCommandSurface(t *testing.T) {
	root := newAppsCmd()
	require.Equal(t, "apps", root.Use)
	subs := map[string]bool{}
	for _, sub := range root.Commands() {
		subs[sub.Name()] = true
	}
	for _, name := range []string{"apply", "list", "show", "diff", "secrets", "deploy", "restart", "stop", "start", "remove", "status", "logs"} {
		assert.True(t, subs[name], "apps subcommand %s", name)
	}

	secrets := newAppsSecretsCmd()
	secretSubs := map[string]bool{}
	for _, sub := range secrets.Commands() {
		secretSubs[sub.Name()] = true
	}
	assert.True(t, secretSubs["set"])
	assert.True(t, secretSubs["delete"])

	flagged := map[*cobra.Command][]string{
		newAppsApplyCmd():         {"file", "dry-run", "deploy", "json"},
		newAppsListCmd():          {"json"},
		newAppsShowCmd():          {"json"},
		newAppsDiffCmd():          {"json"},
		newAppsSecretsSetCmd():    {"service", "stdin", "json"},
		newAppsSecretsDeleteCmd(): {"service", "json"},
		newAppDeployCmd():         {"revision", "service", "json"},
		newAppRestartCmd():        {"service", "json"},
		newAppStopCmd():           {"json"},
		newAppStartCmd():          {"json"},
		newAppRemoveCmd():         {"json"},
		newAppStatusCmd():         {"json"},
		newAppLogsCmd():           {"service", "follow", "tail", "json"},
	}
	for cmd, flags := range flagged {
		for _, flag := range flags {
			assert.NotNil(t, cmd.Flags().Lookup(flag), "%s must define --%s", cmd.Use, flag)
		}
	}

	assert.Equal(t, "deploy APP", newAppDeployCmd().Use)
	assert.Equal(t, "restart APP", newAppRestartCmd().Use)
	assert.Equal(t, "stop APP", newAppStopCmd().Use)
	assert.Equal(t, "start APP", newAppStartCmd().Use)
	assert.Equal(t, "remove APP", newAppRemoveCmd().Use)
	assert.Equal(t, "status APP", newAppStatusCmd().Use)
	assert.Equal(t, "logs APP", newAppLogsCmd().Use)
}

func TestRunAppsDiff_TextAndJSON(t *testing.T) {
	plane := &fakeAppPlane{diffResp: &dto.AppDiffResponse{
		App:  "blog",
		Diff: dto.AppDiffSection{Added: []string{"b"}, Removed: []string{"a"}, Changed: []string{"c"}},
	}}
	var out bytes.Buffer
	require.NoError(t, runAppsDiff(context.Background(), plane, "blog", &out, false))
	text := out.String()
	assert.Contains(t, text, "added:")
	assert.Contains(t, text, "removed:")
	assert.Contains(t, text, "changed:")

	out.Reset()
	require.NoError(t, runAppsDiff(context.Background(), plane, "blog", &out, true))
	var got dto.AppDiffResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, *plane.diffResp, got)

	plane.diffResp = &dto.AppDiffResponse{App: "blog"}
	out.Reset()
	require.NoError(t, runAppsDiff(context.Background(), plane, "blog", &out, false))
	assert.Contains(t, out.String(), "No differences")
}

func TestRunAppsSecretsDelete(t *testing.T) {
	plane := &fakeAppPlane{}
	var out bytes.Buffer
	require.NoError(t, runAppsSecretsDelete(context.Background(), plane, &out, "blog", "password", "web", false))
	assert.Contains(t, out.String(), "password")

	out.Reset()
	require.NoError(t, runAppsSecretsDelete(context.Background(), plane, &out, "blog", "password", "web", true))
	var got map[string]string
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "password", got["deleted"])

	err := runAppsSecretsDelete(context.Background(), plane, &bytes.Buffer{}, "blog", "password", "", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--service")
}

func TestRunAppRestart_AndLifecycle(t *testing.T) {
	resp := &dto.AppDeployResponse{Op: "op-2", App: "blog", Revision: "rev-b", Outcome: "success"}
	plane := &fakeAppPlane{
		deployResp: resp,
		lifecycleFn: func(_ context.Context, app string) (*dto.AppDeployResponse, string, error) {
			assert.Equal(t, "blog", app)
			return resp, "key-lc", nil
		},
	}
	var out bytes.Buffer
	require.NoError(t, runAppRestart(context.Background(), plane, "blog", "", &out, false))
	assert.Contains(t, out.String(), "success")

	out.Reset()
	require.NoError(t, runAppRestart(context.Background(), plane, "blog", "", &out, true))
	var got dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, *resp, got)

	for _, fn := range []appLifecycleFunc{plane.StopApp, plane.StartApp, plane.RemoveApp} {
		out.Reset()
		require.NoError(t, runAppLifecycle(context.Background(), fn, "stop", "blog", &out, false))
		assert.Contains(t, out.String(), "op-2")
	}

	unknown := &fakeAppPlane{
		lifecycleFn: func(_ context.Context, _ string) (*dto.AppDeployResponse, string, error) {
			return nil, "key-x", &remote.OutcomeUnknownError{Method: "POST", Path: "/x", Err: errors.New("boom")}
		},
	}
	err := runAppLifecycle(context.Background(), unknown.StopApp, "stop", "blog", &bytes.Buffer{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key-x")
}

func TestRunAppStatus_Observed(t *testing.T) {
	plane := &fakeAppPlane{showResp: &dto.AppShowResponse{
		App: "blog",
		Active: dto.AppActiveDTO{Converged: true, Services: map[string]dto.AppActiveServiceDTO{
			"web": {EffectiveRevision: "rev-b", Container: "ctr-b"},
		}},
	}}
	var out bytes.Buffer
	require.NoError(t, runAppStatus(context.Background(), plane, "blog", &out, false))
	assert.Contains(t, out.String(), "ctr-b")

	out.Reset()
	require.NoError(t, runAppStatus(context.Background(), plane, "blog", &out, true))
	var got dto.AppShowResponse
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, *plane.showResp, got)
}

// fakeAppLogReader scripts container log reads for app log tests.
type fakeAppLogReader struct {
	lines  []string
	stream []string
}

func (f *fakeAppLogReader) GetContainerLogs(_ context.Context, _ string, _ int) ([]string, error) {
	return f.lines, nil
}

func (f *fakeAppLogReader) StreamContainerLogs(_ context.Context, _ string, _ int) (<-chan string, error) {
	ch := make(chan string, len(f.stream))
	for _, line := range f.stream {
		ch <- line
	}
	close(ch)
	return ch, nil
}

func TestRunAppLogs_TextJSONFollow(t *testing.T) {
	show := &dto.AppShowResponse{App: "blog", Active: dto.AppActiveDTO{Services: map[string]dto.AppActiveServiceDTO{
		"web": {Container: "ctr-web"},
	}}}
	plane := &fakeAppPlane{showResp: show}
	reader := &fakeAppLogReader{lines: []string{"l1", "l2"}, stream: []string{"s1"}}

	var out bytes.Buffer
	require.NoError(t, runAppLogs(context.Background(), plane, reader, "blog", "", false, 50, &out, false))
	assert.Equal(t, "l1\nl2\n", out.String())

	out.Reset()
	require.NoError(t, runAppLogs(context.Background(), plane, reader, "blog", "web", false, 50, &out, true))
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, "blog", got["app"])

	out.Reset()
	require.NoError(t, runAppLogs(context.Background(), plane, reader, "blog", "web", true, 50, &out, false))
	assert.Equal(t, "s1\n", out.String())
}
