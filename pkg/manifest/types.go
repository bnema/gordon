package manifest

// OCIManifest represents an OCI image manifest structure
type OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        OCIDescriptor     `json:"config"`
	Layers        []OCIDescriptor   `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// DockerManifest represents a Docker v2.2 manifest structure
type DockerManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        OCIDescriptor   `json:"config"`
	Layers        []OCIDescriptor `json:"layers"`
}

// OCIDescriptor represents a content descriptor
type OCIDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ManifestList represents a multi-platform manifest list
type ManifestList struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Manifests     []ManifestDescriptor `json:"manifests"`
	Annotations   map[string]string    `json:"annotations,omitempty"`
}

// ManifestDescriptor represents a manifest in a manifest list
type ManifestDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *Platform         `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Platform represents the platform information
type Platform struct {
	Architecture string   `json:"architecture"`
	OS           string   `json:"os"`
	OSVersion    string   `json:"os.version,omitempty"`
	OSFeatures   []string `json:"os.features,omitempty"`
	Variant      string   `json:"variant,omitempty"`
}
