package dto

import "github.com/bnema/gordon/internal/domain"

// CleanupReport describes cleanup results in API responses.
type CleanupReport struct {
	Domain               string              `json:"domain"`
	RemovedContainers    []CleanupContainer  `json:"removed_containers"`
	PreservedVolumes     []CleanupVolume     `json:"preserved_volumes"`
	PreservedAttachments []CleanupAttachment `json:"preserved_attachments"`
	OrphanedEntities     []CleanupOrphaned   `json:"orphaned_entities"`
	Warnings             []string            `json:"warnings"`
	Hints                []string            `json:"hints"`
	PartialFailures      []CleanupFailure    `json:"partial_failures"`
}

// CleanupContainer identifies a container affected by cleanup.
type CleanupContainer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
}

// CleanupVolume identifies preserved stateful data.
type CleanupVolume struct {
	Name          string `json:"name"`
	ContainerPath string `json:"container_path,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// CleanupAttachment identifies an attachment retained for follow-up cleanup.
type CleanupAttachment struct {
	Name        string `json:"name"`
	Image       string `json:"image"`
	ContainerID string `json:"container_id"`
	Status      string `json:"status"`
	Owner       string `json:"owner,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// CleanupOrphaned identifies runtime state no longer configured.
type CleanupOrphaned struct {
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// CleanupFailure records non-fatal cleanup failures.
type CleanupFailure struct {
	Action string `json:"action"`
	Kind   string `json:"kind"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Error  string `json:"error"`
}

// CleanupReportFromDomain maps a domain cleanup report to an API DTO.
func CleanupReportFromDomain(report *domain.CleanupReport) *CleanupReport {
	if report == nil {
		return nil
	}
	out := &CleanupReport{
		Domain:   report.Domain,
		Warnings: append([]string(nil), report.Warnings...),
		Hints:    append([]string(nil), report.Hints...),
	}
	for _, item := range report.RemovedContainers {
		out.RemovedContainers = append(out.RemovedContainers, CleanupContainer(item))
	}
	for _, item := range report.PreservedVolumes {
		out.PreservedVolumes = append(out.PreservedVolumes, CleanupVolume(item))
	}
	for _, item := range report.PreservedAttachments {
		out.PreservedAttachments = append(out.PreservedAttachments, CleanupAttachment(item))
	}
	for _, item := range report.OrphanedEntities {
		out.OrphanedEntities = append(out.OrphanedEntities, CleanupOrphaned(item))
	}
	for _, item := range report.PartialFailures {
		out.PartialFailures = append(out.PartialFailures, CleanupFailure(item))
	}
	return out
}
