package releasesmoke

import (
	"context"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRoleIdentitiesMatchDomainFixedIdentities(t *testing.T) {
	t.Parallel()

	require.Len(t, RoleIdentities, 4)
	for _, ri := range RoleIdentities {
		fixed, ok := domain.FixedComponentProcessIdentity(ri.Role)
		require.True(t, ok, "role %s", ri.Role)
		require.Equal(t, strconv.Itoa(fixed.UID), ri.Identity)
		require.Equal(t, fixed.User, ri.Identity+":"+ri.Identity)
	}
}

func TestDockerImageSmokeServeRoleChecksUseRoleIdentities(t *testing.T) {
	t.Parallel()

	runner := &recordingServeRunner{}
	require.NoError(t, dockerImageSmokeServeRoleChecks(t.Context(), runner, "gordon:test", "amd64"))
	want := make([]string, 0, len(RoleIdentities))
	for _, spec := range RoleIdentities {
		want = append(want, string(spec.Role))
	}
	require.Equal(t, want, runner.serveRoles())
}

type recordingServeRunner struct {
	roles []string
}

func (r *recordingServeRunner) serveRoles() []string {
	return append([]string(nil), r.roles...)
}

func (r *recordingServeRunner) Command(ctx context.Context, _ ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

func (r *recordingServeRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 7 && args[0] == "run" && args[5] == "serve" && args[6] == "--role" && len(args) >= 8 {
		r.roles = append(r.roles, args[7])
	}
	return "", nil
}
