package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

func TestRunAppsApply_DryRunOmitsEmptyRevisionAndRendersDiff(t *testing.T) {
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).Return(&dto.AppApplyResponse{
		App:    "blog",
		Noop:   false,
		Diff:   dto.AppDiffSection{Added: []string{"service.web"}},
		Intent: "apply-7",
	}, nil).Once()
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
	plane := appPlane(t)
	plane.EXPECT().ApplyApp(mock.Anything, mock.Anything).
		Return(&dto.AppApplyResponse{App: "blog", Noop: true}, nil).Once()
	var out bytes.Buffer
	require.NoError(t, runAppsApply(context.Background(), plane, strings.NewReader(""), &out,
		writeManifest(t, "x"), true, false, false))
	assert.Contains(t, out.String(), "No changes for blog")
	assert.NotContains(t, out.String(), "()")
}

func TestRunAppLogs_RejectsJSONWithFollow(t *testing.T) {
	// The JSON/follow rejection happens before any read.
	plane := appPlane(t)
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
	plane := climocks.NewMockControlPlane(t)
	plane.EXPECT().GetStatus(mock.Anything).Return(&remote.Status{
		Apps: 2, RegistryDomain: "g.example.com",
		ContainerStatus: map[string]string{"zeta": "active", "alpha": "stopped"},
	}, nil).Once()
	var out bytes.Buffer
	require.NoError(t, runStatusCmd(context.Background(), plane, &out))
	text := out.String()
	assert.Contains(t, text, "Apps:")
	assert.NotContains(t, text, "Routes:")
	assert.Less(t, strings.Index(text, "alpha"), strings.Index(text, "zeta"))
}
