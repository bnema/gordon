// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	s3storage "github.com/bnema/gordon/internal/adapters/out/s3"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/backup"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/pkg/bytesize"
)

// resolveRuntimeConfig converts a server.runtime config value to a socket path.
// "auto" or "" means auto-detect.
// Named runtimes ("podman", "docker") are resolved to well-known socket paths.
// URI schemes (unix://) are stripped so callers receive a bare path.
func resolveRuntimeConfig(value string) string {
	if value == "" || value == "auto" {
		return ""
	}
	// Named runtimes: resolve to well-known socket paths.
	switch value {
	case "podman":
		// Check XDG_RUNTIME_DIR first (rootless Podman).
		if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
			candidate := filepath.Join(xdg, "podman", "podman.sock")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		// Fallback to system-wide Podman socket.
		return "/run/podman/podman.sock"
	case "docker":
		return "/var/run/docker.sock"
	}
	// Explicit socket path — strip URI scheme if present.
	if socketPath, ok := strings.CutPrefix(value, "unix://"); ok {
		return socketPath
	}
	return value
}

func selectedLocalRuntimeEndpointFromDetection(detection docker.DetectionResult) (*selectedLocalRuntimeEndpoint, error) {
	if strings.TrimSpace(detection.SocketPath) == "" {
		return nil, nil
	}
	return newSelectedLocalRuntimeEndpoint(detection.SocketPath)
}

// createOutputAdapters creates the container runtime and event bus.
func createOutputAdapters(ctx context.Context, log zerowrap.Logger, role Role, runtimeSocket string) (*docker.Runtime, *eventbus.InMemory, error) {
	runtime, eventBus, _, err := createOutputAdaptersFromDetection(ctx, log, role, docker.DetectRuntimeSocket(runtimeSocket))
	return runtime, eventBus, err
}

func createOutputAdaptersFromDetection(ctx context.Context, log zerowrap.Logger, role Role, detection docker.DetectionResult) (*docker.Runtime, *eventbus.InMemory, *selectedLocalRuntimeEndpoint, error) {
	if !roleMayInstantiateRuntimeAdapter(role) {
		return nil, nil, nil, fmt.Errorf("%w: role %q cannot instantiate container runtime", ErrRoleRuntimeOwnership, role)
	}
	endpoint, err := selectedLocalRuntimeEndpointFromDetection(detection)
	if err != nil {
		return nil, nil, nil, err
	}

	var runtime *docker.Runtime

	switch detection.Source {
	case "none":
		return nil, nil, nil, fmt.Errorf("no container runtime found: checked Docker socket, Podman socket, DOCKER_HOST env var. Install Docker or Podman, or set server.runtime in config")
	case "DOCKER_HOST_passthrough":
		runtime, err = docker.NewRuntime()
	default:
		if detection.SocketPath != "" {
			runtime, err = docker.NewRuntimeWithSocket(detection.SocketPath)
		} else {
			runtime, err = docker.NewRuntime()
		}
	}
	if err != nil {
		return nil, nil, nil, log.WrapErr(err, "failed to create container runtime")
	}

	if err := runtime.Ping(ctx); err != nil {
		return nil, nil, nil, log.WrapErr(err, fmt.Sprintf("container runtime not available via %s", detection.Source))
	}

	runtimeVersion, _ := runtime.Version(ctx)
	log.Info().
		Str("version", runtimeVersion).
		Str("source", detection.Source).
		Msg("container runtime initialized")

	eventBus := eventbus.NewInMemory(100, log)

	return runtime, eventBus, endpoint, nil
}

func buildContainerServiceConfig(ctx context.Context, v *viper.Viper, cfg Config, svc *services, log zerowrap.Logger) (container.Config, error) {
	if cfg.Containers.CPULimit < 0 {
		return container.Config{}, fmt.Errorf("containers.cpu_limit must be >= 0 (got %f)", cfg.Containers.CPULimit)
	}
	if cfg.Containers.PidsLimit < 0 {
		return container.Config{}, fmt.Errorf("containers.pids_limit must be >= 0 (got %d)", cfg.Containers.PidsLimit)
	}
	var defaultMemoryLimit int64
	if cfg.Containers.MemoryLimit != "" {
		parsed, err := bytesize.Parse(cfg.Containers.MemoryLimit)
		if err != nil {
			return container.Config{}, fmt.Errorf("invalid containers.memory_limit %q: %w", cfg.Containers.MemoryLimit, err)
		}
		if parsed <= 0 {
			return container.Config{}, fmt.Errorf("containers.memory_limit must be positive (got %q)", cfg.Containers.MemoryLimit)
		}
		defaultMemoryLimit = parsed
	}
	var defaultNanoCPUs int64
	if cfg.Containers.CPULimit > 0 {
		defaultNanoCPUs = int64(cfg.Containers.CPULimit * 1e9)
	}

	attachmentConfig := svc.configSvc.GetAttachmentConfig()
	registryDomain, legacyRegistryDomains := resolveRegistryDomains(cfg)

	containerConfig := container.Config{
		RegistryAuthEnabled:        cfg.Auth.Enabled,
		RegistryDomain:             registryDomain,
		LegacyRegistryDomains:      legacyRegistryDomains,
		RegistryPort:               cfg.Server.RegistryPort,
		InternalRegistryUsername:   svc.internalRegUser,
		InternalRegistryPassword:   svc.internalRegPass,
		PullPolicy:                 v.GetString("deploy.pull_policy"),
		VolumeAutoCreate:           v.GetBool("volumes.auto_create"),
		VolumePrefix:               v.GetString("volumes.prefix"),
		VolumePreserve:             v.GetBool("volumes.preserve"),
		NetworkIsolation:           v.GetBool("network_isolation.enabled"),
		NetworkPrefix:              v.GetString("network_isolation.network_prefix"),
		NetworkGroups:              attachmentConfig.NetworkGroups,
		NetworkInternal:            v.GetBool("network_isolation.internal"),
		Attachments:                attachmentConfig.Attachments,
		AllowedRegistries:          cfg.Images.AllowedRegistries,
		RequireImageDigest:         cfg.Images.RequireDigest,
		SecurityProfile:            cfg.Containers.SecurityProfile,
		ReadinessDelay:             v.GetDuration("deploy.readiness_delay"),
		ReadinessMode:              v.GetString("deploy.readiness_mode"),
		HealthTimeout:              v.GetDuration("deploy.health_timeout"),
		StabilizationDelay:         v.GetDuration("deploy.stabilization_delay"),
		TCPProbeTimeout:            v.GetDuration("deploy.tcp_probe_timeout"),
		HTTPProbeTimeout:           v.GetDuration("deploy.http_probe_timeout"),
		DrainDelay:                 v.GetDuration("deploy.drain_delay"),
		DrainMode:                  v.GetString("deploy.drain_mode"),
		DrainTimeout:               v.GetDuration("deploy.drain_timeout"),
		DefaultMemoryLimit:         defaultMemoryLimit,
		DefaultNanoCPUs:            defaultNanoCPUs,
		DefaultPidsLimit:           cfg.Containers.PidsLimit,
		AttachmentReadinessTimeout: v.GetDuration("deploy.attachment_readiness_timeout"),
	}
	if v.IsSet("deploy.drain_delay") {
		containerConfig.DrainDelayConfigured = true
		containerConfig.DrainDelay = v.GetDuration("deploy.drain_delay")
	}

	if containerConfig.RegistryAuthEnabled {
		if svc.authSvc == nil {
			return container.Config{}, fmt.Errorf("authentication service unavailable: cannot generate registry service token")
		}
		expiry, err := resolveServiceTokenExpiry(cfg)
		if err != nil {
			return container.Config{}, log.WrapErr(err, "failed to resolve service token expiry")
		}
		serviceToken, err := svc.authSvc.GenerateToken(ctx, serviceTokenSubject, []string{"pull"}, expiry)
		if err != nil {
			return container.Config{}, log.WrapErr(err, "failed to generate registry service token")
		}
		log.Info().
			Str("subject", serviceTokenSubject).
			Str("expiry", expiry.String()).
			Msg("generated service token for container registry access")
		containerConfig.ServiceTokenUsername = serviceTokenSubject
		containerConfig.ServiceToken = serviceToken
	} else {
		log.Warn().Msg("registry auth disabled; container image pulls will use unauthenticated mode")
	}

	return containerConfig, nil
}

// createContainerService creates the container service with configuration.
func createContainerService(ctx context.Context, v *viper.Viper, cfg Config, svc *services, log zerowrap.Logger) (*container.Service, error) {
	containerConfig, err := buildContainerServiceConfig(ctx, v, cfg, svc, log)
	if err != nil {
		return nil, err
	}
	return container.NewService(svc.runtime, svc.envLoader, svc.eventBus, svc.logWriter, containerConfig, svc.configSvc), nil
}

type databaseBackupSettingsConfig struct {
	Enabled    bool
	Schedule   string
	StorageDir string
	Retention  struct {
		Hourly  int
		Daily   int
		Weekly  int
		Monthly int
	}
}

func databaseBackupSettings(cfg Config) databaseBackupSettingsConfig {
	out := databaseBackupSettingsConfig{
		Enabled:    cfg.Backups.Databases.Enabled,
		Schedule:   cfg.Backups.Databases.Schedule,
		StorageDir: cfg.Backups.Databases.StorageDir,
	}
	out.Retention.Hourly = cfg.Backups.Databases.Retention.Hourly
	out.Retention.Daily = cfg.Backups.Databases.Retention.Daily
	out.Retention.Weekly = cfg.Backups.Databases.Retention.Weekly
	out.Retention.Monthly = cfg.Backups.Databases.Retention.Monthly
	// Legacy [backups] keys intentionally override new database defaults when
	// backups.enabled is true, preserving existing working pg_dump schedules.
	// Otherwise, prefer the already-populated backups.databases.* values.
	if cfg.Backups.Enabled {
		out.Enabled = true
		if cfg.Backups.Schedule != "" {
			out.Schedule = cfg.Backups.Schedule
		}
		if cfg.Backups.StorageDir != "" {
			out.StorageDir = cfg.Backups.StorageDir
		}
	} else {
		if out.Schedule == "" {
			out.Schedule = cfg.Backups.Schedule
		}
		if out.StorageDir == "" {
			out.StorageDir = cfg.Backups.StorageDir
		}
	}
	if out.Retention.Hourly == 0 {
		out.Retention.Hourly = cfg.Backups.Retention.Hourly
	}
	if out.Retention.Daily == 0 {
		out.Retention.Daily = cfg.Backups.Retention.Daily
	}
	if out.Retention.Weekly == 0 {
		out.Retention.Weekly = cfg.Backups.Retention.Weekly
	}
	if out.Retention.Monthly == 0 {
		out.Retention.Monthly = cfg.Backups.Retention.Monthly
	}
	return out
}

func createBackupService(cfg Config, svc *services, log zerowrap.Logger) (*filesystem.BackupStorage, *backup.Service, error) {
	dbCfg := databaseBackupSettings(cfg)
	if !dbCfg.Enabled {
		return nil, nil, nil
	}

	storageDir := dbCfg.StorageDir
	if storageDir == "" {
		dataDir := resolveDataDir(cfg.Server.DataDir)
		storageDir = filepath.Join(dataDir, "backups")
	}

	backupStorage, err := filesystem.NewBackupStorage(storageDir, log)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create backup storage")
	}

	retention, err := validateBackupRetention(cfg)
	if err != nil {
		return nil, nil, log.WrapErr(err, "invalid backup retention policy")
	}

	backupCfg := domain.BackupConfig{
		Enabled:    dbCfg.Enabled,
		StorageDir: storageDir,
		Retention:  retention,
	}

	backupSvc := backup.NewService(svc.runtime, backupStorage, svc.containerSvc, backupCfg, log)

	log.Info().
		Str("storage_dir", storageDir).
		Msg("backup service initialized")

	return backupStorage, backupSvc, nil
}

func createVolumeBackupService(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) (out.VolumeBackupStorage, *backup.VolumeService, domain.VolumeBackupConfig, error) {
	if !cfg.Backups.Volumes.Enabled {
		return nil, nil, domain.VolumeBackupConfig{}, nil
	}
	volumeCfg, err := validateVolumeBackupConfig(cfg)
	if err != nil {
		return nil, nil, domain.VolumeBackupConfig{}, log.WrapErr(err, "invalid volume backup configuration")
	}

	storage, err := s3storage.NewVolumeBackupStorage(ctx, volumeCfg)
	if err != nil {
		return nil, nil, domain.VolumeBackupConfig{}, log.WrapErr(err, "failed to create volume backup storage")
	}

	volumeSvc := backup.NewVolumeService(svc.runtime, svc.runtime, storage, volumeCfg, log)
	log.Info().
		Str("bucket", volumeCfg.S3Bucket).
		Str("prefix", volumeCfg.S3Prefix).
		Msg("volume backup service initialized")

	return storage, volumeSvc, volumeCfg, nil
}

func validateBackupRetention(cfg Config) (domain.RetentionPolicy, error) {
	dbCfg := databaseBackupSettings(cfg)
	if dbCfg.Retention.Hourly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.hourly cannot be negative")
	}
	if dbCfg.Retention.Daily < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.daily cannot be negative")
	}
	if dbCfg.Retention.Weekly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.weekly cannot be negative")
	}
	if dbCfg.Retention.Monthly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.monthly cannot be negative")
	}

	return domain.RetentionPolicy{
		Hourly:  dbCfg.Retention.Hourly,
		Daily:   dbCfg.Retention.Daily,
		Weekly:  dbCfg.Retention.Weekly,
		Monthly: dbCfg.Retention.Monthly,
	}, nil
}

func validateVolumeBackupConfig(cfg Config) (domain.VolumeBackupConfig, error) {
	volumeCfg := cfg.Backups.Volumes
	interval, err := parsePositiveDurationDefault(volumeCfg.Interval, "24h", "backups.volumes.interval")
	if err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	timeout, err := parsePositiveDurationDefault(volumeCfg.Timeout, "2h", "backups.volumes.timeout")
	if err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	compression, err := parseVolumeBackupCompression(volumeCfg.Compression)
	if err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	maxConcurrency := volumeCfg.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = 2
	}
	helperImage := strings.TrimSpace(volumeCfg.HelperImage)
	if helperImage == "" {
		helperImage = "alpine:3.20"
	}
	volumePrefix := strings.TrimSpace(cfg.Volumes.Prefix)
	if volumePrefix == "" {
		volumePrefix = "gordon"
	}
	if compression == domain.VolumeBackupCompressionZstd && helperImage == "alpine:3.20" {
		return domain.VolumeBackupConfig{}, fmt.Errorf("backups.volumes.compression zstd requires a helper_image that provides zstd")
	}
	if err := validateVolumeBackupS3Settings(volumeCfg.Enabled, volumeCfg.Retention.Keep, maxConcurrency, volumeCfg.S3.Bucket, volumeCfg.S3.Region); err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	return domain.VolumeBackupConfig{
		Enabled:        volumeCfg.Enabled,
		Interval:       interval,
		Compression:    compression,
		Retention:      domain.VolumeBackupRetentionPolicy{Keep: volumeCfg.Retention.Keep},
		Timeout:        timeout,
		MaxConcurrency: maxConcurrency,
		HelperImage:    helperImage,
		VolumePrefix:   volumePrefix,
		S3Bucket:       strings.TrimSpace(volumeCfg.S3.Bucket),
		S3Region:       strings.TrimSpace(volumeCfg.S3.Region),
		S3Prefix:       strings.TrimSpace(volumeCfg.S3.Prefix),
		S3Endpoint:     strings.TrimSpace(volumeCfg.S3.Endpoint),
		S3PathStyle:    volumeCfg.S3.PathStyle,
		S3SSEAlgorithm: strings.TrimSpace(volumeCfg.S3.SSEAlgorithm),
		S3SSEKMSKeyID:  strings.TrimSpace(volumeCfg.S3.SSEKMSKeyID),
	}, nil
}

func parsePositiveDurationDefault(raw, defaultValue, field string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultValue
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", field)
	}
	return d, nil
}

func parseVolumeBackupCompression(raw string) (domain.VolumeBackupCompression, error) {
	if strings.TrimSpace(raw) == "" {
		raw = string(domain.VolumeBackupCompressionGzip)
	}
	compression := domain.VolumeBackupCompression(strings.ToLower(strings.TrimSpace(raw)))
	switch compression {
	case domain.VolumeBackupCompressionGzip, domain.VolumeBackupCompressionZstd:
		return compression, nil
	default:
		return "", fmt.Errorf("backups.volumes.compression must be one of: gzip, zstd")
	}
}

func validateVolumeBackupS3Settings(enabled bool, keep, maxConcurrency int, bucket, region string) error {
	if keep < 0 {
		return fmt.Errorf("backups.volumes.retention.keep cannot be negative")
	}
	if enabled && keep == 0 {
		return fmt.Errorf("backups.volumes.retention.keep must be positive when volume backups are enabled")
	}
	if maxConcurrency < 1 {
		return fmt.Errorf("backups.volumes.max_concurrency must be at least 1")
	}
	if enabled && strings.TrimSpace(bucket) == "" {
		return fmt.Errorf("backups.volumes.s3.bucket is required when volume backups are enabled")
	}
	if enabled && strings.TrimSpace(region) == "" {
		return fmt.Errorf("backups.volumes.s3.region is required when volume backups are enabled")
	}
	return nil
}
