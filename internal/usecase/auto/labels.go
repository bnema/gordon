package auto

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// manifestSchema represents the relevant parts of an OCI/Docker manifest.
type manifestSchema struct {
	SchemaVersion int    `json:"schemaVersion"`
	MediaType     string `json:"mediaType"`
	Config        struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"config"`
}

// ParseConfigDigest extracts the config digest from a manifest.
func ParseConfigDigest(manifestData []byte) (string, error) {
	var manifest manifestSchema
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", err
	}

	return manifest.Config.Digest, nil
}

// imageConfig represents the relevant parts of an OCI/Docker image config.
type imageConfig struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// ParseImageLabels extracts Gordon labels from an image config blob.
func ParseImageLabels(configData []byte) (*domain.ImageLabels, error) {
	var config imageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return nil, err
	}

	labels := &domain.ImageLabels{}

	if config.Config.Labels == nil {
		return labels, nil
	}

	// Extract gordon.* labels
	if v, ok := config.Config.Labels[domain.LabelDomain]; ok {
		labels.Domain = strings.TrimSpace(v)
	}

	if v, ok := config.Config.Labels[domain.LabelDomains]; ok {
		// Parse comma-separated domains
		for _, d := range strings.Split(v, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				labels.Domains = append(labels.Domains, d)
			}
		}
	}

	if v, ok := config.Config.Labels[domain.LabelHealth]; ok {
		labels.Health = strings.TrimSpace(v)
	}

	for _, key := range []string{domain.LabelProxyPort, domain.LabelPort} {
		if v, ok := config.Config.Labels[key]; ok {
			labels.Port = strings.TrimSpace(v)
			break
		}
	}

	if v, ok := config.Config.Labels[domain.LabelEnvFile]; ok {
		labels.EnvFile = strings.TrimSpace(v)
	}

	return labels, nil
}

// ExtractLabels extracts Gordon labels from an image manifest by fetching the
// config blob from blobStorage and parsing it. It is the shared implementation
// used by both AutoRouteHandler and AutoPreviewHandler.
func ExtractLabels(ctx context.Context, manifestData []byte, blobStorage out.BlobStorage) (*domain.ImageLabels, error) {
	configDigest, err := ParseConfigDigest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	if configDigest == "" {
		return nil, fmt.Errorf("no config digest found in manifest")
	}

	reader, err := blobStorage.GetBlob(configDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to get config blob: %w", err)
	}
	defer reader.Close()

	configData, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read config blob: %w", err)
	}

	labels, err := ParseImageLabels(configData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return labels, nil
}
