package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"

	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
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

// MigrationPreflightProbes are narrow, read-only capabilities. Implementations
// must not create networks, pull images, generate credentials, or mutate state.
// Reset is called once per report and is used by production's runtime fact cache.
type MigrationPreflightProbes struct {
	Reset         func()
	Runtime       func(context.Context) (RuntimePreflightTarget, error)
	Image         func(context.Context) error
	Config        func(context.Context) error
	SplitTopology func(context.Context) error
	DataDir       func(context.Context) error
	Registry      func(context.Context) error
	Env           func(context.Context) error
	Secrets       func(context.Context) error
	Ports         func(context.Context) error
	Network       func(context.Context) error
	Inventory     func(context.Context) error
	Disk          func(context.Context) error
	Credentials   func(context.Context) error
}

type MigrationPreflight struct{ probes MigrationPreflightProbes }

func NewMigrationPreflight(probes MigrationPreflightProbes) *MigrationPreflight {
	return &MigrationPreflight{probes: probes}
}

// PreflightProductionDependencies is the control composition seam for
// preflight construction. Production always supplies the real constructor;
// listener tests can inject a failing read-only probe without changing host
// paths, permissions, listeners, or container state.
type PreflightProductionDependencies struct {
	NewMigrationPreflight func(string, Config, out.RuntimeEnvironmentProbe, out.RuntimeStateSubscriber) *MigrationPreflight
}

func productionPreflightProductionDependencies() PreflightProductionDependencies {
	return PreflightProductionDependencies{NewMigrationPreflight: newControlMigrationPreflight}
}

func (d PreflightProductionDependencies) build(configPath string, cfg Config, runtime out.RuntimeEnvironmentProbe, inventory out.RuntimeStateSubscriber) (*MigrationPreflight, error) {
	if d.NewMigrationPreflight == nil {
		return nil, fmt.Errorf("production migration preflight constructor is required")
	}
	preflight := d.NewMigrationPreflight(configPath, cfg, runtime, inventory)
	if preflight == nil {
		return nil, fmt.Errorf("production migration preflight is required")
	}
	return preflight, nil
}

// newControlMigrationPreflight keeps filesystem/config checks in control while
// delegating runtime facts and actual state to authenticated runtime RPCs.
// It deliberately receives no runtime adapter or socket path.
func newControlMigrationPreflight(configPath string, cfg Config, runtime out.RuntimeEnvironmentProbe, inventory out.RuntimeStateSubscriber) *MigrationPreflight {
	dataDir := resolveDataDir(cfg.Server.DataDir)
	facts := &runtimePreflightFacts{probe: runtime, publicPorts: configuredPublicPorts(cfg)}
	probes := MigrationPreflightProbes{
		Reset:         facts.reset,
		Runtime:       facts.runtime,
		Image:         facts.image,
		Config:        configReparseProbe(configPath),
		SplitTopology: splitTopologyProbe(cfg),
		DataDir:       directoryAccessProbe(dataDir, unix.R_OK|unix.W_OK),
		Registry:      directoryAccessProbe(filepath.Join(dataDir, "registry"), unix.R_OK|unix.W_OK),
		Env:           environmentDirectoryProbe(resolveEnvDir(cfg)),
		Secrets:       secretBackendHealthProbe(cfg),
		Ports:         facts.publicListeners,
		Network:       facts.network,
		Inventory:     managedRuntimeInventoryProbe(inventory),
		Disk:          diskSpaceProbe(dataDir),
		Credentials:   credentialStoreHealthProbe(dataDir),
	}
	return NewMigrationPreflight(probes)
}

// Check performs a dry-run only. Probe failures are intentionally not copied
// into the report because runtime/configuration errors can contain credentials.
func (p *MigrationPreflight) Check(ctx context.Context) MigrationPreflightReport {
	if p != nil && p.probes.Reset != nil {
		p.probes.Reset()
	}
	checks := []PreflightCheck{
		p.runtimeCheck(ctx),
		p.probeCheck(ctx, "target_image", PreflightRuntime, "make the configured Gordon image available to the rootless Podman user", p.probes.Image),
		p.probeCheck(ctx, "configuration", PreflightConfig, "fix the configuration error without changing the running deployment", p.probes.Config),
		p.probeCheck(ctx, "split_topology", PreflightConfig, "this deployment is already running in split mode; migrate only converts a monolith deployment", p.probes.SplitTopology),
		p.probeCheck(ctx, "data_directory", PreflightStorage, "make the configured data directory accessible and writable", p.probes.DataDir),
		p.probeCheck(ctx, "registry_storage", PreflightStorage, "create and authorize the configured registry storage directory before migration", p.probes.Registry),
		p.probeCheck(ctx, "environment_directory", PreflightEnv, "create and authorize the configured environment directory and required files before migration", p.probes.Env),
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
	if err != nil || !strings.EqualFold(strings.TrimSpace(target.Engine), "podman") || !target.Rootless || !target.APIReachable {
		check.Status = PreflightFail
		return check
	}
	check.Status = PreflightPass
	return check
}

func (p *MigrationPreflight) probeCheck(ctx context.Context, name string, category PreflightCategory, remediation string, probe func(context.Context) error) PreflightCheck {
	check := PreflightCheck{Name: name, Category: category, Remediation: remediation}
	if p == nil || probe == nil || probe(ctx) != nil {
		check.Status = PreflightFail
		return check
	}
	check.Status = PreflightPass
	return check
}

// runtimePreflightFacts shares one authenticated runtime probe across the
// runtime, image, and network checks of a single report. It exposes no raw
// runtime errors or endpoints to callers.
// runtimeEnvironmentAndPublicListenerProbe is implemented by the authenticated
// gRPC runtime client. It keeps all migration facts in one snapshot RPC; the
// fallback maintains compatibility with runtime probes that predate it.
type runtimeEnvironmentAndPublicListenerProbe interface {
	ProbeRuntimeEnvironmentWithPublicListeners(context.Context, []int) (out.RuntimeEnvironment, []bool, error)
}

type runtimePreflightFacts struct {
	mu          sync.Mutex
	probe       out.RuntimeEnvironmentProbe
	publicPorts []int
	listeners   []bool
	loaded      bool
	target      RuntimePreflightTarget
	err         error
}

func (f *runtimePreflightFacts) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded, f.target, f.listeners, f.err = false, RuntimePreflightTarget{}, nil, nil
}

func (f *runtimePreflightFacts) load(ctx context.Context) (RuntimePreflightTarget, error) {
	if f == nil || f.probe == nil {
		return RuntimePreflightTarget{}, fmt.Errorf("runtime probe unavailable")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.loaded {
		var report out.RuntimeEnvironment
		var listeners []bool
		var err error
		if combined, ok := f.probe.(runtimeEnvironmentAndPublicListenerProbe); ok {
			report, listeners, err = combined.ProbeRuntimeEnvironmentWithPublicListeners(ctx, f.publicPorts)
		} else {
			report, err = f.probe.ProbeRuntimeEnvironment(ctx)
		}
		f.target = RuntimePreflightTarget{Engine: report.Engine, Rootless: report.Rootless, APIReachable: report.APIReachable, ImageAvailable: report.ImageAvailable, ImagePullable: report.ImagePullable, NetworkFeasible: report.NetworkFeasible, DiskAvailable: report.DiskAvailable, DiskSufficient: report.DiskSufficient}
		f.listeners, f.err, f.loaded = listeners, err, true
	}
	return f.target, f.err
}

func (f *runtimePreflightFacts) runtime(ctx context.Context) (RuntimePreflightTarget, error) {
	return f.load(ctx)
}
func (f *runtimePreflightFacts) image(ctx context.Context) error {
	target, err := f.load(ctx)
	if err != nil || !target.ImageAvailable || !target.ImagePullable {
		return fmt.Errorf("target image unavailable")
	}
	return nil
}
func (f *runtimePreflightFacts) network(ctx context.Context) error {
	target, err := f.load(ctx)
	if err != nil || !target.NetworkFeasible {
		return fmt.Errorf("component network unavailable")
	}
	return nil
}

func (f *runtimePreflightFacts) publicListeners(ctx context.Context) error {
	if _, err := f.load(ctx); err != nil {
		return fmt.Errorf("runtime listener ownership unavailable")
	}
	f.mu.Lock()
	listeners := append([]bool(nil), f.listeners...)
	ports := append([]int(nil), f.publicPorts...)
	f.mu.Unlock()
	if len(ports) == 0 {
		return nil
	}
	if listeners == nil {
		probe, ok := f.probe.(out.RuntimePublicListenerProbe)
		if !ok {
			return fmt.Errorf("runtime listener ownership probe unavailable")
		}
		available, err := probe.ProbePublicListeners(ctx, ports)
		if err != nil {
			return fmt.Errorf("runtime listener ownership unavailable")
		}
		listeners = available
	}
	if len(listeners) != len(ports) {
		return fmt.Errorf("runtime listener ownership unavailable")
	}
	for _, accepted := range listeners {
		if !accepted {
			return fmt.Errorf("configured public listener is unavailable")
		}
	}
	return nil
}

// splitTopologyProbe fails closed when configuration already declares an
// external control plane. migrate converts a running monolith into split
// components; a deployment that is already split has no monolith to convert and
// must instead be provisioned from scratch. It reuses SplitModeEnabled so the
// guard and the kernel split-mode rejection share a single detection signal.
func splitTopologyProbe(cfg Config) func(context.Context) error {
	return func(context.Context) error {
		if SplitModeEnabled(cfg) {
			return fmt.Errorf("deployment already runs in split mode")
		}
		return nil
	}
}

func configReparseProbe(configPath string) func(context.Context) error {
	return func(context.Context) error {
		if strings.TrimSpace(configPath) == "" {
			return fmt.Errorf("configuration path unavailable")
		}
		_, cfg, err := initConfig(configPath)
		if err != nil {
			return err
		}
		_, err = resolveSecretsBackend(cfg.Auth.SecretsBackend)
		return err
	}
}

// directoryAccessProbe uses Lstat and unix.Access so configured storage must
// already exist, be a real directory, and be accessible without creating it.
func directoryAccessProbe(path string, mode uint32) func(context.Context) error {
	return func(context.Context) error {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("directory path unavailable")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("configured path must be a non-symlink directory")
		}
		if err := unix.Access(path, mode); err != nil {
			return err
		}
		return nil
	}
}

func environmentDirectoryProbe(path string) func(context.Context) error {
	base := directoryAccessProbe(path, unix.R_OK)
	return func(ctx context.Context) error {
		if err := base(ctx); err != nil {
			return err
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() && !entry.IsDir() {
				return fmt.Errorf("environment entry must be a regular file or directory")
			}
			info, err := os.Lstat(filepath.Join(path, entry.Name()))
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("environment entry unavailable")
			}
			if info.Mode().IsRegular() && unix.Access(filepath.Join(path, entry.Name()), unix.R_OK) != nil {
				return fmt.Errorf("environment file unreadable")
			}
		}
		return nil
	}
}

// secretBackendHealthProbe asks only backend metadata/health. It never reads a
// configured secret, invokes GetSecret, or copies a secret value into a report.
func secretBackendHealthProbe(cfg Config) func(context.Context) error {
	return func(ctx context.Context) error {
		backend, err := resolveSecretsBackend(cfg.Auth.SecretsBackend)
		if err != nil {
			return err
		}
		switch backend {
		case domain.SecretsBackendPass:
			if !secrets.NewPassProvider(zerowrap.Default()).IsAvailable() {
				return fmt.Errorf("pass backend unavailable")
			}
		case domain.SecretsBackendSops:
			if !secrets.NewSopsProvider(zerowrap.Default()).IsAvailable() {
				return fmt.Errorf("sops backend unavailable")
			}
		case domain.SecretsBackendUnsafe:
			return environmentDirectoryProbe(resolveEnvDir(cfg))(ctx)
		default:
			return fmt.Errorf("secret backend unavailable")
		}
		return nil
	}
}

// publicListenerProbe delegates listener ownership to runtime. Control never
// reads /proc or a container socket: every configured port must be free or be
// confirmed by runtime as held by a running Gordon-managed monolith.
func publicListenerProbe(runtime out.RuntimeEnvironmentProbe, cfg Config) func(context.Context) error {
	ports := configuredPublicPorts(cfg)
	return func(ctx context.Context) error {
		probe, ok := runtime.(out.RuntimePublicListenerProbe)
		if !ok {
			return fmt.Errorf("runtime listener ownership probe unavailable")
		}
		available, err := probe.ProbePublicListeners(ctx, ports)
		if err != nil || len(available) != len(ports) {
			return fmt.Errorf("runtime listener ownership unavailable")
		}
		for _, accepted := range available {
			if !accepted {
				return fmt.Errorf("configured public listener is unavailable")
			}
		}
		return nil
	}
}

func configuredPublicPorts(cfg Config) []int {
	seen := make(map[int]struct{})
	add := func(port int) {
		if port > 0 {
			seen[port] = struct{}{}
		}
	}
	add(cfg.Server.Port)
	add(cfg.Server.RegistryPort)
	add(effectivePublicTLSPort(cfg))
	for _, entry := range cfg.EntryPoints {
		add(portFromAddress(entry.Address))
	}
	ports := make([]int, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	return ports
}

// managedRuntimeInventoryProbe consumes the existing sanitized actual-state
// stream; it never opens the runtime socket or asks for raw inspect payloads.
func managedRuntimeInventoryProbe(subscriber out.RuntimeStateSubscriber) func(context.Context) error {
	return func(ctx context.Context) error {
		if subscriber == nil {
			return fmt.Errorf("runtime inventory unavailable")
		}
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		updates, err := subscriber.SubscribeRuntimeState(ctx)
		if err != nil {
			return err
		}
		select {
		case snapshot, ok := <-updates:
			if !ok {
				return fmt.Errorf("runtime inventory stream closed")
			}
			return snapshot.Validate()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func diskSpaceProbe(path string) func(context.Context) error {
	return func(context.Context) error {
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil || stat.Bsize <= 0 {
			return fmt.Errorf("storage filesystem unavailable")
		}
		available := stat.Bavail * uint64(stat.Bsize)
		if available < 1<<30 {
			return fmt.Errorf("insufficient storage")
		}
		return nil
	}
}

// credentialStoreHealthProbe validates the existing parent where component
// credentials would be atomically stored. It deliberately does not mint a
// token, create a directory, or write a health sentinel.
func credentialStoreHealthProbe(dataDir string) func(context.Context) error {
	return directoryAccessProbe(dataDir, unix.R_OK|unix.W_OK)
}
