package domain

// Label keys used by Gordon for container and image metadata.
const (
	// Container labels
	LabelDomain  = "gordon.domain"
	LabelImage   = "gordon.image"
	LabelManaged = "gordon.managed"
	LabelCreated = "gordon.created"
	// LabelEnvHash stores a SHA-256 hash of the effective environment
	// variables at deploy time, used to detect env drift without
	// exposing secret values.
	LabelEnvHash = "gordon.env-hash"

	// App ownership labels are stamped by the v2.50 deploy engine on every
	// container and volume it creates (03-deployment.md §4A/B, frozen here
	// so prune/backup guards can consume them before the engine activates).
	// The engine wiring that stamps them lands at cutover; until then no
	// live resource carries them and guards treat their absence on a
	// managed resource as legacy/unknown provenance (never prune-eligible
	// once app state exists).
	LabelApp         = "gordon.app"
	LabelAppService  = "gordon.app.service"
	LabelAppRevision = "gordon.app.revision"
	// LabelAppID carries the app's stable internal UUID, so provenance
	// survives a public-name reuse without ever inferring ownership
	// from the name alone.
	LabelAppID = "gordon.app.id"

	// Standalone service labels identify Gordon-managed L4 service containers.
	LabelService                       = "gordon.service"
	LabelServiceName                   = "gordon.service.name"
	LabelServiceConfigHash             = "gordon.service.config-hash"
	LabelServiceManagedVolumes         = "gordon.service.managed-volumes"
	LabelServiceCleanupPreserveVolumes = "gordon.service.cleanup.preserve-volumes"
	LabelServiceCleanupRemoveContainer = "gordon.service.cleanup.remove-container"

	// LabelProxyPort specifies the container port to proxy HTTP traffic to.
	LabelProxyPort = "gordon.proxy.port"

	// LabelProxyProtocol specifies the backend protocol the container speaks.
	// Supported values: "h2c" (cleartext HTTP/2). When unset, HTTP/1.1 is assumed.
	LabelProxyProtocol = "gordon.proxy.protocol"

	// Auto-route image labels (for automatic route discovery)
	// LabelDomains specifies multiple route domains (comma-separated).
	LabelDomains = "gordon.domains"
	// LabelHealth specifies the health check endpoint path.
	LabelHealth = "gordon.health"
	// LabelPort is a deprecated alias for LabelProxyPort.
	// Kept for backward compatibility; prefer LabelProxyPort for new images.
	LabelPort = "gordon.port"
	// LabelEnvFile specifies the path to .env file inside the image.
	LabelEnvFile = "gordon.env-file"
)
