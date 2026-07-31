package container

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

type expectedLifecycleMount struct {
	source   string
	options  []string
	readOnly bool
}

// componentMountPlan is the single mount contract for component create and inspect attestation.
type componentMountPlan struct {
	mounts map[string]expectedLifecycleMount
}

func (p componentMountPlan) expectedMounts() map[string]expectedLifecycleMount {
	copied := make(map[string]expectedLifecycleMount, len(p.mounts))
	for destination, mount := range p.mounts {
		copied[destination] = mount
	}
	return copied
}

func (p componentMountPlan) applyToConfig(config *domain.ContainerConfig) {
	if config == nil {
		return
	}
	writable := make(map[string]string)
	readOnly := make(map[string]string)
	for destination, mount := range p.mounts {
		if mount.readOnly {
			readOnly[destination] = mount.source
			continue
		}
		writable[destination] = mount.source
	}
	config.Volumes = writable
	config.ReadOnlyVolumes = readOnly
	if len(p.mounts) == 0 {
		return
	}
	volumeOptions := make(map[string][]string)
	for destination, mount := range p.mounts {
		if len(mount.options) > 0 {
			volumeOptions[destination] = append([]string(nil), mount.options...)
		}
	}
	if len(volumeOptions) > 0 {
		config.VolumeOptions = volumeOptions
	}
}

type componentMountPlanInput struct {
	command             domain.RuntimeSelfUpdateCommand
	configFile          string
	runtimeSocketSource string
	managedSecrets      string
	registryStorageRoot string
	migrationStateRoot  string
}

func buildComponentMountPlan(input componentMountPlanInput) (componentMountPlan, error) {
	migrationID := strings.TrimPrefix(input.command.PolicyDecisionID, "migration:")
	if !domain.ValidComponentMigrationID(migrationID) {
		return componentMountPlan{}, fmt.Errorf("invalid component migration identity: %w", domain.ErrInvalidComponentMountPlan)
	}
	plan := componentMountPlan{mounts: map[string]expectedLifecycleMount{
		"/etc/gordon/role.toml": {source: input.configFile, readOnly: true},
	}}
	addPersistentComponentMounts(&plan, input)
	if input.command.TargetComponentRole == domain.ComponentRoleRuntime && strings.TrimSpace(input.runtimeSocketSource) != "" {
		plan.mounts["/run/gordon/runtime.sock"] = expectedLifecycleMount{source: input.runtimeSocketSource, readOnly: true}
	}
	if err := addRegistryStorageMount(&plan, input); err != nil {
		return componentMountPlan{}, err
	}
	if err := addMigrationRuntimeSocketStateMounts(&plan, input, migrationID); err != nil {
		return componentMountPlan{}, err
	}
	if err := addMigrationComponentConfigStateMounts(&plan, input); err != nil {
		return componentMountPlan{}, err
	}
	return plan, nil
}

func addPersistentComponentMounts(plan *componentMountPlan, input componentMountPlanInput) {
	if input.command.TargetComponentRole == domain.ComponentRoleEdge {
		return
	}
	volumeName := domain.FormatComponentGenerationVolumeName(
		input.command.TargetComponentRole,
		strings.TrimPrefix(input.command.PolicyDecisionID, "migration:"),
		input.command.Generation,
	)
	plan.mounts["/var/lib/gordon"] = expectedLifecycleMount{
		source:  volumeName,
		options: componentGenerationVolumeOptions(input.command, map[string]string{"/var/lib/gordon": volumeName})["/var/lib/gordon"],
	}
	if input.command.TargetComponentRole == domain.ComponentRoleControl && domain.ValidManagedControlSecretsVolume(strings.TrimSpace(input.managedSecrets)) {
		plan.mounts[managedControlSecretsPath] = expectedLifecycleMount{source: strings.TrimSpace(input.managedSecrets)}
	}
}

func addRegistryStorageMount(plan *componentMountPlan, input componentMountPlanInput) error {
	if input.command.TargetComponentRole != domain.ComponentRoleRegistry || strings.TrimSpace(input.registryStorageRoot) == "" {
		return nil
	}
	root := filepath.Clean(input.registryStorageRoot)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return fmt.Errorf("canonical registry storage is not configured: %w", domain.ErrInvalidComponentMountPlan)
	}
	plan.mounts = map[string]expectedLifecycleMount{
		"/etc/gordon/role.toml":    {source: input.configFile, readOnly: true},
		"/var/lib/gordon/registry": {source: root},
	}
	return nil
}

func addMigrationRuntimeSocketStateMounts(plan *componentMountPlan, input componentMountPlanInput, migrationID string) error {
	if input.command.TargetComponentRole != domain.ComponentRoleRuntime && input.command.TargetComponentRole != domain.ComponentRoleControl {
		return nil
	}
	if strings.TrimSpace(input.migrationStateRoot) == "" {
		return nil
	}
	root := filepath.Clean(input.migrationStateRoot)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("migration runtime socket root is not configured: %w", domain.ErrInvalidComponentMountPlan)
	}
	state := filepath.Join(root, migrationID)
	destination := filepath.Join("/var/lib/gordon/migration", migrationID)
	if input.command.TargetComponentRole == domain.ComponentRoleRuntime {
		plan.mounts[destination] = expectedLifecycleMount{source: state}
		return nil
	}
	plan.mounts[destination] = expectedLifecycleMount{source: state, readOnly: true}
	plan.mounts[filepath.Join(destination, "attestation")] = expectedLifecycleMount{source: filepath.Join(state, "attestation")}
	return nil
}

func addMigrationComponentConfigStateMounts(plan *componentMountPlan, input componentMountPlanInput) error {
	if input.command.TargetComponentRole != domain.ComponentRoleRuntime || strings.TrimSpace(input.migrationStateRoot) == "" {
		return nil
	}
	root := filepath.Clean(input.migrationStateRoot)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("migration component configuration root is not configured: %w", domain.ErrInvalidComponentMountPlan)
	}
	for _, name := range []string{"config", "env"} {
		path := filepath.Join(root, name)
		plan.mounts[path] = expectedLifecycleMount{source: path, readOnly: true}
	}
	return nil
}

func (m *runtimeComponentLifecycleManager) componentMountPlanInput(
	command domain.RuntimeSelfUpdateCommand,
	configFile string,
	runtimeSocketSource string,
) componentMountPlanInput {
	return componentMountPlanInput{
		command:             command,
		configFile:          configFile,
		runtimeSocketSource: runtimeSocketSource,
		managedSecrets:      m.policy.ManagedControlSecretsVolume,
		registryStorageRoot: m.policy.RegistryStorageRoot,
		migrationStateRoot:  m.policy.MigrationStateRoot,
	}
}

func (m *runtimeComponentLifecycleManager) componentMountPlanForCreate(command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish) (componentMountPlan, error) {
	configFile, err := componentLifecycleConfigFile(command, ports, m.policy.MigrationStateRoot)
	if err != nil {
		return componentMountPlan{}, err
	}
	runtimeSocketSource := ""
	if command.TargetComponentRole == domain.ComponentRoleRuntime {
		environment, envErr := componentLifecycleEnvironment(command, command.EnvironmentFile, m.policy.MigrationStateRoot)
		if envErr != nil {
			return componentMountPlan{}, envErr
		}
		source, _, socketErr := runtimeComponentSocketMount(environment)
		if socketErr != nil {
			return componentMountPlan{}, socketErr
		}
		runtimeSocketSource = source
	}
	return buildComponentMountPlan(m.componentMountPlanInput(command, configFile, runtimeSocketSource))
}

func (m *runtimeComponentLifecycleManager) componentMountPlanForAttestation(command domain.RuntimeSelfUpdateCommand, configFile string, runtimeSocketSource string) (componentMountPlan, error) {
	return buildComponentMountPlan(m.componentMountPlanInput(command, configFile, runtimeSocketSource))
}
