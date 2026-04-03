package domain

// HealthCheckUserAgentPrefix is the User-Agent prefix set by Gordon's internal
// health-check prober. Shared so access-log filtering and the prober stay in sync.
const HealthCheckUserAgentPrefix = "Gordon-HealthCheck/"

// Label keys used by Gordon for container and image metadata.
const (
	// Container labels
	LabelDomain     = "gordon.domain"
	LabelImage      = "gordon.image"
	LabelManaged    = "gordon.managed"
	LabelRoute      = "gordon.route"
	LabelAttachment = "gordon.attachment"
	LabelAttachedTo = "gordon.attached-to"
	LabelCreated    = "gordon.created"
	// LabelEnvHash stores a SHA-256 hash of the effective environment
	// variables at deploy time, used to detect env drift without
	// exposing secret values.
	LabelEnvHash = "gordon.env-hash"

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
