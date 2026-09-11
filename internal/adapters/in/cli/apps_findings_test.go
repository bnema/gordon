package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestRunAppsApply_DryRunOmitsEmptyRevisionAndRendersDiff(t *testing.T) {
	plane := &fakeAppPlane{applyResp: &dto.AppApplyResponse{
		App:    "blog",
		Noop:   false,
		Diff:   dto.AppDiffSection{Added: []string{"service.web"}},
		Intent: "apply-7",
	}}
	var out bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out,
		writeManifest(t, "[app]\nname = \"blog\"\n"), true, false, false))
	text := out.String()
	assert.Contains(t, text, "Validated (dry-run) blog")
	assert.NotContains(t, text, "()")
	assert.Contains(t, text, "added:")
	assert.Contains(t, text, "service.web")
}

func TestRunAppsApply_NoopOmitsEmptyRevision(t *testing.T) {
	plane := &fakeAppPlane{applyResp: &dto.AppApplyResponse{App: "blog", Noop: true}}
	var out bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out,
		writeManifest(t, "x"), true, false, false))
	assert.Contains(t, out.String(), "No changes for blog")
	assert.NotContains(t, out.String(), "()")
}

func TestRunAppLogs_RejectsJSONWithFollow(t *testing.T) {
	plane := &fakeAppPlane{showResp: &dto.AppShowResponse{App: "blog"}}
	err := runAppLogs(context.Background(), plane, &fakeAppLogReader{}, "blog", "web", true, 50, &bytes.Buffer{}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--json")
	assert.Contains(t, err.Error(), "--follow")
}

func TestAppLogRef_ZeroServicesAccurateError(t *testing.T) {
	empty := &dto.AppShowResponse{App: "new"}
	_, err := appLogRef(empty, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active services")
}

func TestRunStatusCmd_AppsLabelAndSortedHelpers(t *testing.T) {
	plane := &statusFakePlane{status: &remote.Status{
		Apps: 2, RegistryDomain: "g.example.com",
		ContainerStatus: map[string]string{"zeta": "active", "alpha": "stopped"},
	}}
	var out bytes.Buffer
	require.NoError(t, runStatusCmd(context.Background(), plane, &out))
	text := out.String()
	assert.Contains(t, text, "Apps:")
	assert.NotContains(t, text, "Routes:")
	assert.Less(t, strings.Index(text, "alpha"), strings.Index(text, "zeta"))
}

type statusFakePlane struct {
	ControlPlane
	status *remote.Status
	err    error
}

func (f *statusFakePlane) GetStatus(context.Context) (*remote.Status, error) {
	return f.status, f.err
}
