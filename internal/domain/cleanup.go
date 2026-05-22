package domain

// CleanupReport describes the result of reconciling runtime state after a
// configuration entity was removed. Fields are additive-only for CLI/API JSON
// consumers; callers should tolerate unknown future fields.
type CleanupReport struct {
	Domain               string                  `json:"domain"`
	RemovedContainers    []CleanupContainer      `json:"removed_containers"`
	PreservedVolumes     []CleanupVolume         `json:"preserved_volumes"`
	PreservedAttachments []CleanupAttachment     `json:"preserved_attachments"`
	OrphanedEntities     []CleanupOrphanedEntity `json:"orphaned_entities"`
	Warnings             []string                `json:"warnings"`
	Hints                []string                `json:"hints"`
	PartialFailures      []CleanupFailure        `json:"partial_failures"`
}

// CleanupContainer identifies a container affected by cleanup.
type CleanupContainer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
}

// CleanupVolume identifies stateful data intentionally preserved by cleanup.
type CleanupVolume struct {
	Name          string `json:"name"`
	ContainerPath string `json:"container_path,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// CleanupAttachment identifies an attachment intentionally preserved or kept for
// explicit follow-up cleanup.
type CleanupAttachment struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	ContainerID string `json:"container_id"`
	Status      string `json:"status"`
	Owner       string `json:"owner,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// CleanupOrphanedEntity identifies runtime state not currently configured.
type CleanupOrphanedEntity struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// CleanupFailure records non-fatal cleanup failures that require follow-up.
type CleanupFailure struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Error  string `json:"error"`
}
