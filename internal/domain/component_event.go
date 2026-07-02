package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ComponentEventType identifies an event exchanged between Gordon components.
type ComponentEventType string

const (
	ComponentEventTypeRegistryImagePushed ComponentEventType = "registry.image_pushed"
	ComponentEventTypeRuntimeStateChanged ComponentEventType = "runtime.state_changed"
	ComponentEventTypeRuntimeDeploy       ComponentEventType = "runtime.deploy"
	ComponentEventTypeEdgeDrain           ComponentEventType = "edge.drain"
)

// ComponentEventPayloadKind identifies the serialization shape carried by a component event.
type ComponentEventPayloadKind string

const (
	ComponentEventPayloadKindJSON     ComponentEventPayloadKind = "json"
	ComponentEventPayloadKindProtobuf ComponentEventPayloadKind = "protobuf"
	ComponentEventPayloadKindEmpty    ComponentEventPayloadKind = "empty"
)

// ComponentEventAuditClassification describes how a component event should be handled for audit purposes.
type ComponentEventAuditClassification string

const (
	ComponentEventAuditNone     ComponentEventAuditClassification = "none"
	ComponentEventAuditRead     ComponentEventAuditClassification = "read"
	ComponentEventAuditWrite    ComponentEventAuditClassification = "write"
	ComponentEventAuditSecurity ComponentEventAuditClassification = "security"
	ComponentEventAuditCritical ComponentEventAuditClassification = "critical"
)

var (
	// ErrInvalidComponentEvent is returned when a component event envelope is malformed.
	ErrInvalidComponentEvent = errors.New("invalid component event")
)

// ComponentEventEnvelope is the domain-level envelope for events exchanged by Gordon components.
// It is intentionally transport-neutral; adapters choose how to encode and deliver it.
type ComponentEventEnvelope struct {
	ID                  string
	Type                ComponentEventType
	Origin              ComponentRole
	Timestamp           time.Time
	Generation          uint64
	IdempotencyKey      string
	PayloadKind         ComponentEventPayloadKind
	SerializedPayload   []byte
	RetryCount          int
	AuditClassification ComponentEventAuditClassification
}

// Validate checks the component event envelope invariants.
func (e ComponentEventEnvelope) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidComponentEvent)
	}
	if strings.TrimSpace(string(e.Type)) == "" {
		return fmt.Errorf("%w: type is required", ErrInvalidComponentEvent)
	}
	if !isKnownComponentEventType(e.Type) {
		return fmt.Errorf("%w: type is invalid", ErrInvalidComponentEvent)
	}
	if !IsKnownComponentRole(e.Origin) {
		return fmt.Errorf("%w: origin is invalid", ErrInvalidComponentEvent)
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("%w: timestamp is required", ErrInvalidComponentEvent)
	}
	if e.RetryCount < 0 {
		return fmt.Errorf("%w: retry count must be non-negative", ErrInvalidComponentEvent)
	}
	if !isKnownComponentEventPayloadKind(e.PayloadKind) {
		return fmt.Errorf("%w: payload kind is invalid", ErrInvalidComponentEvent)
	}
	if !e.payloadCoherent() {
		return fmt.Errorf("%w: payload kind is not coherent with serialized payload", ErrInvalidComponentEvent)
	}
	if !isKnownComponentEventAuditClassification(e.AuditClassification) {
		return fmt.Errorf("%w: audit classification is invalid", ErrInvalidComponentEvent)
	}
	if e.requiresIdempotencyKey() && strings.TrimSpace(e.IdempotencyKey) == "" {
		return fmt.Errorf("%w: idempotency key is required for write/security/critical events", ErrInvalidComponentEvent)
	}
	if e.AuditClassification == ComponentEventAuditNone && isCriticalComponentEventType(e.Type) {
		return fmt.Errorf("%w: audit classification is not coherent with critical event type", ErrInvalidComponentEvent)
	}
	return nil
}

// DedupeKey returns a stable key for local idempotence. It intentionally excludes timestamp
// and retry metadata so re-delivery of the same logical event produces the same key.
func (e ComponentEventEnvelope) DedupeKey() string {
	identity := strings.TrimSpace(e.IdempotencyKey)
	if identity == "" {
		identity = e.ID
	}
	h := sha256.New()
	_, _ = h.Write([]byte(identity))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(e.Type))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(e.Origin))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatUint(e.Generation, 10)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(e.PayloadKind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(e.SerializedPayload)
	return hex.EncodeToString(h.Sum(nil))
}

func isKnownComponentEventType(eventType ComponentEventType) bool {
	switch eventType {
	case ComponentEventTypeRegistryImagePushed,
		ComponentEventTypeRuntimeStateChanged,
		ComponentEventTypeRuntimeDeploy,
		ComponentEventTypeEdgeDrain:
		return true
	default:
		return false
	}
}

func (e ComponentEventEnvelope) payloadCoherent() bool {
	if e.PayloadKind == ComponentEventPayloadKindEmpty {
		return len(e.SerializedPayload) == 0
	}
	return len(e.SerializedPayload) > 0
}

func (e ComponentEventEnvelope) requiresIdempotencyKey() bool {
	return e.AuditClassification == ComponentEventAuditWrite ||
		e.AuditClassification == ComponentEventAuditSecurity ||
		e.AuditClassification == ComponentEventAuditCritical ||
		isCriticalComponentEventType(e.Type)
}

func isKnownComponentEventPayloadKind(kind ComponentEventPayloadKind) bool {
	switch kind {
	case ComponentEventPayloadKindJSON, ComponentEventPayloadKindProtobuf, ComponentEventPayloadKindEmpty:
		return true
	default:
		return false
	}
}

func isKnownComponentEventAuditClassification(classification ComponentEventAuditClassification) bool {
	switch classification {
	case ComponentEventAuditNone, ComponentEventAuditRead, ComponentEventAuditWrite, ComponentEventAuditSecurity, ComponentEventAuditCritical:
		return true
	default:
		return false
	}
}

func isCriticalComponentEventType(eventType ComponentEventType) bool {
	switch eventType {
	case ComponentEventTypeRuntimeDeploy, ComponentEventTypeEdgeDrain:
		return true
	default:
		return false
	}
}
