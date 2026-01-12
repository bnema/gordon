package manifest

import (
	"encoding/json"
	"fmt"
)

// ParseManifestAnnotations extracts annotations from manifest data
func ParseManifestAnnotations(manifestData []byte, contentType string) (map[string]string, error) {
	switch contentType {
	case "application/vnd.oci.image.manifest.v1+json":
		return parseOCIManifestAnnotations(manifestData)
	case "application/vnd.docker.distribution.manifest.v2+json":
		// Docker v2.2 manifests don't support annotations at manifest level
		return map[string]string{}, nil
	case "application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
		return parseManifestListAnnotations(manifestData)
	default:
		return map[string]string{}, fmt.Errorf("unsupported manifest media type: %s", contentType)
	}
}

// parseOCIManifestAnnotations parses annotations from an OCI image manifest
func parseOCIManifestAnnotations(manifestData []byte) (map[string]string, error) {
	var manifest OCIManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse OCI manifest: %w", err)
	}

	if manifest.Annotations == nil {
		return map[string]string{}, nil
	}

	return manifest.Annotations, nil
}

// parseManifestListAnnotations parses annotations from a manifest list
func parseManifestListAnnotations(manifestData []byte) (map[string]string, error) {
	var manifestList ManifestList
	if err := json.Unmarshal(manifestData, &manifestList); err != nil {
		return nil, fmt.Errorf("failed to parse manifest list: %w", err)
	}

	if manifestList.Annotations == nil {
		return map[string]string{}, nil
	}

	return manifestList.Annotations, nil
}

// IsVersionedDeployment checks if manifest contains version annotations for deployment
func IsVersionedDeployment(annotations map[string]string) bool {
	// Just check for the "version" annotation - keep it simple
	_, exists := annotations["version"]
	return exists
}

// GetDeploymentVersion extracts the deployment version from annotations
func GetDeploymentVersion(annotations map[string]string) string {
	// Just use the "version" annotation - keep it simple
	if version, exists := annotations["version"]; exists && version != "" {
		return version
	}

	return ""
}
