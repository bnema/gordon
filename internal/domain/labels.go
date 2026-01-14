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

	// Image labels (set in Dockerfile)
	LabelProxyPort = "gordon.proxy.port"
)
