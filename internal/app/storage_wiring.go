// Package app provides the application initialization and wiring.
package app

import (
	"path/filepath"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/filesystem"
)

// createStorage creates blob and manifest storage.
func createStorage(cfg Config, log zerowrap.Logger) (*filesystem.BlobStorage, *filesystem.ManifestStorage, error) {
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}

	registryDir := filepath.Join(dataDir, "registry")

	blobStorage, err := filesystem.NewBlobStorage(registryDir, log)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create blob storage")
	}

	manifestStorage, err := filesystem.NewManifestStorage(registryDir, log)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create manifest storage")
	}

	return blobStorage, manifestStorage, nil
}

func resolveDataDir(dataDir string) string {
	if dataDir == "" {
		return DefaultDataDir()
	}
	return dataDir
}
