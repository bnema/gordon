// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"
	"golang.org/x/sys/unix"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func createDomainSecretStore(cfg Config, log zerowrap.Logger) (string, domain.SecretsBackend, *domainsecrets.PassStore, out.DomainSecretStore, error) {
	envDir := resolveEnvDir(cfg)
	backend, err := resolveSecretsBackend(cfg.Auth.SecretsBackend)
	if err != nil {
		return "", "", nil, nil, log.WrapErr(err, "failed to resolve secrets backend")
	}

	switch backend {
	case domain.SecretsBackendPass:
		passStore, err := domainsecrets.NewPassStore(log)
		if err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to create pass domain secret store")
		}
		if err := migrateEnvFilesToPass(envDir, passStore, log); err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to migrate env files to pass")
		}
		return envDir, backend, passStore, passStore, nil
	default:
		store, err := domainsecrets.NewFileStore(envDir, log)
		if err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to create domain secret store")
		}
		return envDir, backend, nil, store, nil
	}
}

func resolveSecretsBackend(backend string) (domain.SecretsBackend, error) {
	switch backend {
	case "pass":
		return domain.SecretsBackendPass, nil
	case "sops":
		return domain.SecretsBackendSops, nil
	case "unsafe":
		return domain.SecretsBackendUnsafe, nil
	case "":
		return "", fmt.Errorf("auth.secrets_backend is required")
	default:
		return "", fmt.Errorf("unsupported auth.secrets_backend %q", backend)
	}
}

func resolveEnvDir(cfg Config) string {
	dataDir := resolveDataDir(cfg.Server.DataDir)
	envDir := cfg.Env.Dir
	if envDir == "" {
		envDir = filepath.Join(dataDir, "env")
	}
	return envDir
}

// createEnvLoader creates the environment loader with secret providers.
func createEnvLoader(backend domain.SecretsBackend, envDir string, passStore *domainsecrets.PassStore, log zerowrap.Logger) (out.EnvLoader, error) {
	switch backend {
	case domain.SecretsBackendPass:
		loader, err := envloader.NewPassLoader(passStore, log)
		if err != nil {
			return nil, log.WrapErr(err, "failed to create pass env loader")
		}
		return loader, nil
	default:
		loader, err := envloader.NewFileLoader(envDir, log)
		if err != nil {
			return nil, log.WrapErr(err, "failed to create env loader")
		}

		// Register secret providers
		passProvider := secrets.NewPassProvider(log)
		if passProvider.IsAvailable() {
			loader.RegisterSecretProvider(passProvider)
			log.Debug().Msg("pass secret provider registered")
		}

		sopsProvider := secrets.NewSopsProvider(log)
		if sopsProvider.IsAvailable() {
			loader.RegisterSecretProvider(sopsProvider)
			log.Debug().Msg("sops secret provider registered")
		}

		return loader, nil
	}
}

// loadSecret loads a secret from the configured backend.
func loadSecret(ctx context.Context, backend domain.SecretsBackend, path, dataDir string, log zerowrap.Logger) (string, error) {
	switch backend {
	case domain.SecretsBackendPass:
		provider := secrets.NewPassProvider(log)
		return provider.GetSecret(ctx, path)
	case domain.SecretsBackendSops:
		provider := secrets.NewSopsProvider(log)
		return provider.GetSecret(ctx, path)
	case domain.SecretsBackendUnsafe:
		// For unsafe backend, path is relative to dataDir/secrets/.
		return readUnsafeSecret(dataDir, path)
	default:
		return "", fmt.Errorf("unknown secrets backend: %s", backend)
	}
}

func readFileBeneath(root, cleanedRelPath string) ([]byte, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open secrets root: %w", err)
	}
	defer unix.Close(rootFD)

	parts := strings.Split(filepath.ToSlash(cleanedRelPath), "/")
	dirFD := rootFD
	var closeDirFDs []int
	defer func() {
		for i := len(closeDirFDs) - 1; i >= 0; i-- {
			_ = unix.Close(closeDirFDs[i])
		}
	}()

	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid secret path: path must stay under dataDir/secrets")
		}
		last := i == len(parts)-1
		if last {
			fd, err := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return nil, fmt.Errorf("failed to read secret file: %w", err)
			}
			defer unix.Close(fd)
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				return nil, fmt.Errorf("failed to stat secret file: %w", err)
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG {
				return nil, fmt.Errorf("invalid secret path: secret must be a regular file")
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/self/fd/%d", fd))
			if err != nil {
				return nil, fmt.Errorf("failed to read secret file: %w", err)
			}
			return data, nil
		}

		nextFD, err := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to open secret path component: %w", err)
		}
		closeDirFDs = append(closeDirFDs, nextFD)
		dirFD = nextFD
	}

	return nil, fmt.Errorf("invalid secret path: empty path")
}

type unsafeSecretProvider struct {
	dataDir string
}

func (p unsafeSecretProvider) Name() string { return string(domain.SecretsBackendUnsafe) }

func (p unsafeSecretProvider) IsAvailable() bool { return true }

func (p unsafeSecretProvider) GetSecret(_ context.Context, path string) (string, error) {
	return readUnsafeSecret(p.dataDir, path)
}

func createStandaloneServiceSecretProvider(backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) out.SecretProvider {
	switch backend {
	case domain.SecretsBackendPass:
		return secrets.NewPassProvider(log)
	case domain.SecretsBackendSops:
		return secrets.NewSopsProvider(log)
	case domain.SecretsBackendUnsafe:
		return unsafeSecretProvider{dataDir: dataDir}
	default:
		return nil
	}
}

func readUnsafeSecret(dataDir, secretPath string) (string, error) {
	if filepath.IsAbs(secretPath) {
		return "", fmt.Errorf("invalid secret path: absolute paths are not allowed")
	}
	cleaned := filepath.Clean(secretPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid secret path: path must stay under dataDir/secrets")
	}

	root := filepath.Clean(filepath.Join(dataDir, "secrets"))
	data, err := readFileBeneath(root, cleaned)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
