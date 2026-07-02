package domain

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

var (
	// ErrInvalidRuntimeState is returned when a runtime state snapshot is malformed or leaks adapter details.
	ErrInvalidRuntimeState = errors.New("invalid runtime state")
)

// RuntimeActualStateSnapshot is the sanitized actual-state view published by the runtime.
type RuntimeActualStateSnapshot struct {
	Generation        uint64
	StateVersion      string
	SourceComponentID string
	ObservedAt        time.Time
	Routes            []RuntimeRouteState
	Containers        []RuntimeContainerState
	Networks          []RuntimeNetworkState
	Volumes           []RuntimeVolumeState
	EdgeAttachments   []RuntimeEdgeNetworkAttachmentState
}

// Validate checks snapshot invariants and edge-target sanitization.
func (s RuntimeActualStateSnapshot) Validate() error {
	if s.Generation == 0 {
		return fmt.Errorf("%w: generation is required", ErrInvalidRuntimeState)
	}
	if strings.TrimSpace(s.StateVersion) == "" {
		return fmt.Errorf("%w: state version is required", ErrInvalidRuntimeState)
	}
	if strings.TrimSpace(s.SourceComponentID) == "" {
		return fmt.Errorf("%w: source component id is required", ErrInvalidRuntimeState)
	}
	for _, route := range s.Routes {
		if err := route.Validate(); err != nil {
			return err
		}
	}
	for _, container := range s.Containers {
		if err := container.Validate(); err != nil {
			return err
		}
	}
	for _, network := range s.Networks {
		if err := network.Validate(); err != nil {
			return err
		}
	}
	for _, volume := range s.Volumes {
		if err := volume.Validate(); err != nil {
			return err
		}
	}
	for _, attachment := range s.EdgeAttachments {
		if err := attachment.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// RuntimeRouteState is the alias-based route target state consumed by split edge components.
type RuntimeRouteState struct {
	Domain               string
	Generation           uint64
	RouteVersion         string
	ContainerAlias       string
	EdgeTargetAlias      string
	TargetPort           int
	Scheme               string
	Protocol             RouteTargetProtocol
	Status               RouteTargetStatus
	UnavailableReason    RouteTargetUnavailableReason
	BackingContainerName string
}

// Validate checks that route state is sanitized and does not expose localhost host-port routing.
func (s RuntimeRouteState) Validate() error {
	if !IsValidRouteDomain(s.Domain) {
		return fmt.Errorf("%w: route domain is invalid", ErrInvalidRuntimeState)
	}
	if s.Generation == 0 {
		return fmt.Errorf("%w: route generation is required", ErrInvalidRuntimeState)
	}
	if !validRuntimeRouteStatus(s.Status) {
		return fmt.Errorf("%w: route status is invalid", ErrInvalidRuntimeState)
	}
	if !validRuntimeRouteScheme(s.Scheme) {
		return fmt.Errorf("%w: route scheme is invalid", ErrInvalidRuntimeState)
	}
	if !validRuntimeRouteProtocol(s.Protocol) {
		return fmt.Errorf("%w: route protocol is invalid", ErrInvalidRuntimeState)
	}
	if !validRuntimeUnavailableReason(s.UnavailableReason) {
		return fmt.Errorf("%w: unavailable reason is invalid", ErrInvalidRuntimeState)
	}
	return s.validateTargetCoherence()
}

func (s RuntimeRouteState) validateTargetCoherence() error {
	switch s.Status {
	case RouteTargetStatusUnavailable:
		return s.validateUnavailableTarget()
	case RouteTargetStatusReady:
		if s.UnavailableReason != RouteTargetUnavailableReasonNone {
			return fmt.Errorf("%w: ready route cannot include unavailable reason", ErrInvalidRuntimeState)
		}
	case RouteTargetStatusDraining:
		if s.UnavailableReason != RouteTargetUnavailableReasonDraining {
			return fmt.Errorf("%w: draining route requires draining reason", ErrInvalidRuntimeState)
		}
	}
	return s.validateRoutableTarget()
}

func (s RuntimeRouteState) validateUnavailableTarget() error {
	if s.UnavailableReason == RouteTargetUnavailableReasonNone {
		return fmt.Errorf("%w: unavailable route requires reason", ErrInvalidRuntimeState)
	}
	if s.UnavailableReason == RouteTargetUnavailableReasonDraining {
		return fmt.Errorf("%w: unavailable route cannot use draining reason", ErrInvalidRuntimeState)
	}
	if s.ContainerAlias != "" || s.EdgeTargetAlias != "" || s.TargetPort != 0 || s.Scheme != "" || s.Protocol != "" || s.BackingContainerName != "" {
		return fmt.Errorf("%w: unavailable route cannot include endpoint fields", ErrInvalidRuntimeState)
	}
	return nil
}

func (s RuntimeRouteState) validateRoutableTarget() error {
	if strings.TrimSpace(s.EdgeTargetAlias) == "" {
		return fmt.Errorf("%w: edge target alias is required", ErrInvalidRuntimeState)
	}
	if isLocalhostAlias(s.EdgeTargetAlias) {
		return fmt.Errorf("%w: edge target alias cannot be localhost", ErrInvalidRuntimeState)
	}
	if s.Scheme == "" {
		return fmt.Errorf("%w: route scheme is required for routable target", ErrInvalidRuntimeState)
	}
	if s.Protocol == "" {
		return fmt.Errorf("%w: route protocol is required for routable target", ErrInvalidRuntimeState)
	}
	if !validRouteTargetPort(s.TargetPort) {
		return fmt.Errorf("%w: target port must be between 1 and 65535", ErrInvalidRuntimeState)
	}
	return nil
}

// RuntimeContainerState is sanitized container state without full runtime inspect payloads.
type RuntimeContainerState struct {
	Name       string
	Alias      string
	Image      string
	ImageID    string
	Status     ContainerStatus
	StartedAt  time.Time
	Labels     map[string]string
	Generation uint64
}

// Validate checks that container state contains only sanitized public metadata.
func (s RuntimeContainerState) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: container name is required", ErrInvalidRuntimeState)
	}
	if !validRuntimeContainerStatus(s.Status) {
		return fmt.Errorf("%w: container status is invalid", ErrInvalidRuntimeState)
	}
	for key := range s.Labels {
		if !isAllowedRuntimeStateLabel(key) {
			return fmt.Errorf("%w: container label %q is not allowed in runtime state", ErrInvalidRuntimeState, key)
		}
	}
	return nil
}

// SanitizedLabels returns a copy of labels safe for runtime actual-state snapshots.
func (s RuntimeContainerState) SanitizedLabels() map[string]string {
	return SanitizeRuntimeStateLabels(s.Labels)
}

// RuntimeNetworkState is sanitized runtime network state.
type RuntimeNetworkState struct {
	Name       string
	Driver     string
	Internal   bool
	Aliases    []string
	Generation uint64
}

// Validate checks network state contains no host-local endpoint details.
func (s RuntimeNetworkState) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: network name is required", ErrInvalidRuntimeState)
	}
	for _, alias := range s.Aliases {
		if isLocalhostAlias(alias) {
			return fmt.Errorf("%w: network alias cannot be localhost", ErrInvalidRuntimeState)
		}
	}
	return nil
}

// RuntimeVolumeState is sanitized volume state.
type RuntimeVolumeState struct {
	Name       string
	AttachedTo []string
	Generation uint64
}

// Validate checks volume state does not expose host filesystem paths.
func (s RuntimeVolumeState) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: volume name is required", ErrInvalidRuntimeState)
	}
	if strings.ContainsAny(s.Name, `/\\`) {
		return fmt.Errorf("%w: volume name cannot be a host path", ErrInvalidRuntimeState)
	}
	return nil
}

// RuntimeEdgeNetworkAttachmentState describes edge connectivity using network aliases, not host ports.
type RuntimeEdgeNetworkAttachmentState struct {
	RouteDomain     string
	NetworkName     string
	EdgeAlias       string
	RuntimeAlias    string
	TargetAlias     string
	TargetPort      int
	Attached        bool
	Generation      uint64
	SourceComponent string
}

// Validate checks edge attachment fields are alias-based and do not point to localhost.
func (s RuntimeEdgeNetworkAttachmentState) Validate() error {
	if !IsValidRouteDomain(s.RouteDomain) {
		return fmt.Errorf("%w: route domain is invalid", ErrInvalidRuntimeState)
	}
	if strings.TrimSpace(s.TargetAlias) == "" {
		return fmt.Errorf("%w: target alias is required", ErrInvalidRuntimeState)
	}
	if isLocalhostAlias(s.TargetAlias) {
		return fmt.Errorf("%w: target alias cannot be localhost", ErrInvalidRuntimeState)
	}
	if !validRouteTargetPort(s.TargetPort) {
		return fmt.Errorf("%w: target port must be between 1 and 65535", ErrInvalidRuntimeState)
	}
	return nil
}

// RuntimeDeploymentEvent is a sanitized deployment lifecycle event emitted by the runtime.
type RuntimeDeploymentEvent struct {
	ID                string
	RouteDomain       string
	Generation        uint64
	SourceComponentID string
	Status            RuntimeCommandStatus
	Message           string
	OccurredAt        time.Time
}

// RuntimePolicyDeniedEvent records a policy denial without exposing raw runtime payloads.
type RuntimePolicyDeniedEvent struct {
	ID                string
	CommandID         RuntimeCommandID
	RouteDomain       string
	Service           string
	Generation        uint64
	SourceComponentID string
	PolicyDecisionID  string
	Reason            string
	Message           string
	OccurredAt        time.Time
}

func validRuntimeRouteStatus(status RouteTargetStatus) bool {
	switch status {
	case RouteTargetStatusReady, RouteTargetStatusUnavailable, RouteTargetStatusDraining:
		return true
	default:
		return false
	}
}

func validRuntimeRouteScheme(scheme string) bool {
	return scheme == "" || scheme == "http" || scheme == "https"
}

func validRuntimeRouteProtocol(protocol RouteTargetProtocol) bool {
	return protocol == "" || protocol == RouteTargetProtocolHTTP1 || protocol == RouteTargetProtocolH2C
}

func validRuntimeUnavailableReason(reason RouteTargetUnavailableReason) bool {
	switch reason {
	case RouteTargetUnavailableReasonNone,
		RouteTargetUnavailableReasonNoTarget,
		RouteTargetUnavailableReasonStarting,
		RouteTargetUnavailableReasonHealthCheckFailed,
		RouteTargetUnavailableReasonDeployment,
		RouteTargetUnavailableReasonDraining:
		return true
	default:
		return false
	}
}

func validRuntimeContainerStatus(status ContainerStatus) bool {
	switch status {
	case "", ContainerStatusRunning, ContainerStatusStopped, ContainerStatusCreated, ContainerStatusExited, ContainerStatusPaused, ContainerStatusUnknown:
		return true
	default:
		return false
	}
}

// SanitizeRuntimeStateLabels returns only public Gordon labels suitable for actual-state snapshots.
func SanitizeRuntimeStateLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	sanitized := make(map[string]string)
	for key, value := range labels {
		if isAllowedRuntimeStateLabel(key) {
			sanitized[key] = value
		}
	}
	return sanitized
}

func isAllowedRuntimeStateLabel(label string) bool {
	switch label {
	case LabelDomain,
		LabelImage,
		LabelManaged,
		LabelRoute,
		LabelAttachment,
		LabelAttachedTo,
		LabelCreated,
		LabelEnvHash,
		LabelService,
		LabelServiceName,
		LabelServiceConfigHash,
		LabelServiceManagedVolumes,
		LabelServiceCleanupPreserveVolumes,
		LabelServiceCleanupRemoveContainer,
		LabelProxyPort,
		LabelProxyProtocol,
		LabelBackupEnabled,
		LabelBackupType,
		LabelBackupVersion,
		LabelBackupSchedule,
		LabelBackupSidecar:
		return true
	default:
		return false
	}
}

func isLocalhostAlias(alias string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(alias))
	if trimmed == "localhost" {
		return true
	}
	ip := net.ParseIP(trimmed)
	return ip != nil && ip.IsLoopback()
}
