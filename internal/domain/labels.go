package domain

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

	// LabelProxyPort is a deprecated alias for LabelPort.
	// Kept for backward compatibility; prefer LabelPort for new images.
	LabelProxyPort = "gordon.proxy.port"

	// Auto-route image labels (for automatic route discovery)
	// LabelDomains specifies multiple route domains (comma-separated).
	LabelDomains = "gordon.domains"
	// LabelHealth specifies the health check endpoint path.
	LabelHealth = "gordon.health"
	// LabelPort specifies the container port to proxy.
	LabelPort = "gordon.port"
	// LabelEnvFile specifies the path to .env file inside the image.
	LabelEnvFile = "gordon.env-file"
)
