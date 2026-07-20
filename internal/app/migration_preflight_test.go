package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMigrationPreflightPassFailMatrixIsReadOnlyAndRedacted(t *testing.T) {
	calls := 0
	probes := passingMigrationProbes(&calls)
	report := NewMigrationPreflight(probes).Check(context.Background())
	assert.True(t, report.Ready)
	assert.Len(t, report.Checks, 13)
	assert.Equal(t, 13, calls)

	for _, name := range []string{"runtime", "image", "config", "split_topology", "data", "registry", "env", "secrets", "ports", "network", "inventory", "disk", "credentials"} {
		t.Run(name, func(t *testing.T) {
			failing := passingMigrationProbes(nil)
			setFailingMigrationProbe(&failing, name)
			report := NewMigrationPreflight(failing).Check(context.Background())
			assert.False(t, report.Ready)
			hasFailure := false
			for _, check := range report.Checks {
				if check.Status == PreflightFail {
					hasFailure = true
				}
			}
			assert.True(t, hasFailure)
			for _, check := range report.Checks {
				assert.NotContains(t, check.Remediation, "not-to-be-reported")
			}
		})
	}
}

func TestMigrationPreflightRejectsNonRootlessOrUnavailableRuntime(t *testing.T) {
	cases := []struct {
		name   string
		target RuntimePreflightTarget
		err    error
	}{
		{name: "docker", target: RuntimePreflightTarget{Engine: "docker", Rootless: true, APIReachable: true, ImageAvailable: true, ImagePullable: true, NetworkFeasible: true, DiskSufficient: true}},
		{name: "rootful podman", target: RuntimePreflightTarget{Engine: "podman", APIReachable: true, ImageAvailable: true, ImagePullable: true, NetworkFeasible: true, DiskSufficient: true}},
		{name: "runtime unavailable", err: errors.New("runtime endpoint unavailable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := passingMigrationProbes(nil)
			probes.Runtime = func(context.Context) (RuntimePreflightTarget, error) { return tc.target, tc.err }
			report := NewMigrationPreflight(probes).Check(context.Background())
			assert.False(t, report.Ready)
			assert.Equal(t, PreflightFail, report.Checks[0].Status)
			assert.NotContains(t, report.Checks[0].Remediation, "endpoint")
		})
	}
}

func passingMigrationProbes(calls *int) MigrationPreflightProbes {
	probe := func(context.Context) error {
		if calls != nil {
			*calls++
		}
		return nil
	}
	return MigrationPreflightProbes{Runtime: func(context.Context) (RuntimePreflightTarget, error) {
		if calls != nil {
			*calls++
		}
		return RuntimePreflightTarget{Engine: "podman", Rootless: true, APIReachable: true, ImageAvailable: true, ImagePullable: true, NetworkFeasible: true, DiskAvailable: 1 << 30, DiskSufficient: true}, nil
	}, Image: probe, Config: probe, SplitTopology: probe, DataDir: probe, Registry: probe, Env: probe, Secrets: probe, Ports: probe, Network: probe, Inventory: probe, Disk: probe, Credentials: probe}
}
func setFailingMigrationProbe(probes *MigrationPreflightProbes, name string) {
	fail := func(context.Context) error { return errors.New("token=not-to-be-reported") }
	switch name {
	case "runtime":
		probes.Runtime = func(context.Context) (RuntimePreflightTarget, error) {
			return RuntimePreflightTarget{Engine: "docker", Rootless: true}, nil
		}
	case "image":
		probes.Image = fail
	case "config":
		probes.Config = fail
	case "split_topology":
		probes.SplitTopology = fail
	case "data":
		probes.DataDir = fail
	case "registry":
		probes.Registry = fail
	case "env":
		probes.Env = fail
	case "secrets":
		probes.Secrets = fail
	case "ports":
		probes.Ports = fail
	case "network":
		probes.Network = fail
	case "inventory":
		probes.Inventory = fail
	case "disk":
		probes.Disk = fail
	case "credentials":
		probes.Credentials = fail
	}
}
