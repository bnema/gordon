package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// PreflightStatus is deliberately small so reports remain stable for CLI and API clients.
type PreflightStatus string

const (
	PreflightPass    PreflightStatus = "pass"
	PreflightFail    PreflightStatus = "fail"
	PreflightWarning PreflightStatus = "warning"
)

// PreflightCategory groups checks without exposing implementation details such as
// runtime endpoints, environment values, or secret provider arguments.
type PreflightCategory string

const (
	PreflightRuntime     PreflightCategory = "runtime"
	PreflightConfig      PreflightCategory = "config"
	PreflightStorage     PreflightCategory = "storage"
	PreflightEnv         PreflightCategory = "env"
	PreflightPorts       PreflightCategory = "ports"
	PreflightNetwork     PreflightCategory = "network"
	PreflightCredentials PreflightCategory = "credentials"
	PreflightDisk        PreflightCategory = "disk"
	PreflightState       PreflightCategory = "existing_state"
)

type PreflightCheck struct {
	Name        string            `json:"name"`
	Category    PreflightCategory `json:"category"`
	Status      PreflightStatus   `json:"status"`
	Remediation string            `json:"remediation"`
}

type MigrationPreflightReport struct {
	Checks []PreflightCheck `json:"checks"`
	Ready  bool             `json:"ready"`
}

// RuntimePreflightTarget is the sanitized result of a runtime-owned discovery.
// A Docker-compatible API is acceptable only when it positively identifies
// rootless Podman; control code never opens a runtime socket itself.
type RuntimePreflightTarget struct {
	Engine          string
	Rootless        bool
	APIReachable    bool
	ImageAvailable  bool
	ImagePullable   bool
	NetworkFeasible bool
	DiskAvailable   uint64
	DiskSufficient  bool
}

// MigrationPreflightProbes are narrow, read-only capabilities.  Implementations
// must not create networks, pull images, generate credentials, or mutate state.
type MigrationPreflightProbes struct {
	Runtime     func(context.Context) (RuntimePreflightTarget, error)
	Image       func(context.Context) error
	Config      func(context.Context) error
	DataDir     func(context.Context) error
	Registry    func(context.Context) error
	Env         func(context.Context) error
	Secrets     func(context.Context) error
	Ports       func(context.Context) error
	Network     func(context.Context) error
	Inventory   func(context.Context) error
	Disk        func(context.Context) error
	Credentials func(context.Context) error
}

type MigrationPreflight struct{ probes MigrationPreflightProbes }

func NewMigrationPreflight(probes MigrationPreflightProbes) *MigrationPreflight {
	return &MigrationPreflight{probes: probes}
}

// newControlMigrationPreflight keeps host filesystem/config checks in control
// while delegating every runtime fact to the authenticated runtime RPC client.
func newControlMigrationPreflight(cfg Config, runtime out.RuntimeEnvironmentProbe) *MigrationPreflight {
	dataDir := resolveDataDir(cfg.Server.DataDir)
	readDirectory := func(path string) func(context.Context) error {
		return func(context.Context) error {
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("directory unavailable")
			}
			return nil
		}
	}
	runtimeProbe := func(ctx context.Context) (RuntimePreflightTarget, error) {
		if runtime == nil {
			return RuntimePreflightTarget{}, fmt.Errorf("runtime probe unavailable")
		}
		report, err := runtime.ProbeRuntimeEnvironment(ctx)
		if err != nil {
			return RuntimePreflightTarget{}, err
		}
		return RuntimePreflightTarget{Engine: report.Engine, Rootless: report.Rootless, APIReachable: report.APIReachable, ImageAvailable: report.ImageAvailable, ImagePullable: report.ImagePullable, NetworkFeasible: report.NetworkFeasible, DiskAvailable: report.DiskAvailable, DiskSufficient: report.DiskSufficient}, nil
	}
	noError := func(context.Context) error { return nil }
	probes := MigrationPreflightProbes{
		Runtime: runtimeProbe, Image: noError, Config: noError, DataDir: readDirectory(dataDir),
		Registry: readDirectory(filepath.Join(dataDir, "registry")), Env: noError, Secrets: noError,
		Ports: noError, Network: noError, Inventory: noError, Disk: noError, Credentials: noError,
	}
	if strings.TrimSpace(cfg.Env.Dir) != "" {
		probes.Env = readDirectory(cfg.Env.Dir)
	}
	return NewMigrationPreflight(probes)
}

// Check performs a dry-run only.  Probe failures are intentionally not copied
// into the report because runtime/configuration errors can contain credentials.
func (p *MigrationPreflight) Check(ctx context.Context) MigrationPreflightReport {
	checks := []PreflightCheck{
		p.runtimeCheck(ctx),
		p.probeCheck(ctx, "target_image", PreflightRuntime, "make the configured Gordon image available to the rootless Podman user", p.probes.Image),
		p.probeCheck(ctx, "configuration", PreflightConfig, "fix the configuration error without changing the running deployment", p.probes.Config),
		p.probeCheck(ctx, "data_directory", PreflightStorage, "make the configured data directory accessible and writable", p.probes.DataDir),
		p.probeCheck(ctx, "registry_storage", PreflightStorage, "make registry storage accessible and writable", p.probes.Registry),
		p.probeCheck(ctx, "environment_directory", PreflightEnv, "make the configured environment directory readable", p.probes.Env),
		p.probeCheck(ctx, "secret_backend", PreflightEnv, "configure an available secret provider before migration", p.probes.Secrets),
		p.probeCheck(ctx, "public_ports", PreflightPorts, "free public ports or keep them owned by the current Gordon deployment", p.probes.Ports),
		p.probeCheck(ctx, "component_network", PreflightNetwork, "ensure the internal component network name is available", p.probes.Network),
		p.probeCheck(ctx, "managed_inventory", PreflightState, "resolve unmanaged or ambiguous Gordon resources before migration", p.probes.Inventory),
		p.probeCheck(ctx, "disk_space", PreflightDisk, "free enough disk space for the target component images", p.probes.Disk),
		p.probeCheck(ctx, "component_credentials", PreflightCredentials, "configure storage that can safely hold component credentials", p.probes.Credentials),
	}
	report := MigrationPreflightReport{Checks: checks, Ready: true}
	for _, check := range checks {
		if check.Status == PreflightFail {
			report.Ready = false
		}
	}
	return report
}

func (p *MigrationPreflight) runtimeCheck(ctx context.Context) PreflightCheck {
	check := PreflightCheck{Name: "rootless_podman", Category: PreflightRuntime, Remediation: "install and start rootless Podman; Docker-only runtimes are not supported for production migration"}
	if p == nil || p.probes.Runtime == nil {
		check.Status = PreflightFail
		return check
	}
	target, err := p.probes.Runtime(ctx)
	if err != nil || !strings.EqualFold(strings.TrimSpace(target.Engine), "podman") || !target.Rootless || !target.APIReachable || !target.ImageAvailable || !target.ImagePullable || !target.NetworkFeasible || !target.DiskSufficient {
		check.Status = PreflightFail
		return check
	}
	check.Status = PreflightPass
	return check
}

func (p *MigrationPreflight) probeCheck(ctx context.Context, name string, category PreflightCategory, remediation string, probe func(context.Context) error) PreflightCheck {
	check := PreflightCheck{Name: name, Category: category, Remediation: remediation}
	if probe == nil || probe(ctx) != nil {
		check.Status = PreflightFail
		return check
	}
	check.Status = PreflightPass
	return check
}
