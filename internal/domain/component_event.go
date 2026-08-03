package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ComponentEventType identifies a closed set of events exchanged by Gordon components.
type ComponentEventType string

const (
	ComponentEventTypeRegistryImagePushed ComponentEventType = "registry.image_pushed"
	ComponentEventTypeRuntimeStateChanged ComponentEventType = "runtime.state_changed"
	ComponentEventTypeRuntimeDeploy       ComponentEventType = "runtime.deploy"
	ComponentEventTypeContainerDeployed   ComponentEventType = "container.deployed"
	ComponentEventTypeConfigReload        ComponentEventType = "config.reload"
	ComponentEventTypeSecretsChanged      ComponentEventType = "secrets.changed"
	ComponentEventTypeManualDeploy        ComponentEventType = "manual.deploy"
	ComponentEventTypePolicyDenied        ComponentEventType = "policy.denied"
	ComponentEventTypeAudit               ComponentEventType = "audit"
	ComponentEventTypeEdgeDrain           ComponentEventType = "edge.drain"
)

// ComponentEventAuditClassification describes how a component event is handled for audit purposes.
type ComponentEventAuditClassification string

const (
	ComponentEventAuditNone     ComponentEventAuditClassification = "none"
	ComponentEventAuditRead     ComponentEventAuditClassification = "read"
	ComponentEventAuditWrite    ComponentEventAuditClassification = "write"
	ComponentEventAuditSecurity ComponentEventAuditClassification = "security"
	ComponentEventAuditCritical ComponentEventAuditClassification = "critical"
)

var ErrInvalidComponentEvent = errors.New("invalid component event")

// ComponentEventPayload is a closed, typed payload set. It intentionally has an
// unexported method so arbitrary JSON, environment, secret values, and inspect
// output cannot cross this trust boundary.
type ComponentEventPayload interface {
	componentEventPayload()
	Validate() error
	fingerprint() string
}

// RegistryImagePushedPayload carries the exact OCI inputs consumed by image
// automation. Bounds keep the component-event transport from becoming a
// general-purpose blob channel.
type RegistryImagePushedPayload struct {
	Repository  string
	Reference   string
	Digest      string
	Manifest    []byte
	Annotations map[string]string
}

const (
	maxComponentEventManifestBytes = 4 << 20
	maxComponentEventAnnotations   = 64
	maxComponentEventAnnotationKey = 256
	maxComponentEventAnnotationVal = 4096
	maxComponentEventAnnotationAll = 64 << 10
)

type RuntimeStateChangedPayload struct{ ComponentID, State string }
type RuntimeDeployPayload struct {
	Domain, Image string
	Generation    uint64
}
type ContainerDeployedPayload struct {
	Domain, Image, DeploymentID string
	Generation                  uint64
}
type ComponentConfigReloadPayload struct{ Version string }

// ComponentSecretsChangedPayload deliberately carries only a non-secret revision, never names or values.
type ComponentSecretsChangedPayload struct{ Version string }
type ComponentManualDeployPayload struct{ Domain, Image, CorrelationID string }
type PolicyDeniedPayload struct{ DecisionID, Action, Reason string }
type AuditPayload struct{ Action, Subject, Outcome string }
type EdgeDrainPayload struct {
	Domain     string
	Generation uint64
}

func (RegistryImagePushedPayload) componentEventPayload()     {}
func (RuntimeStateChangedPayload) componentEventPayload()     {}
func (RuntimeDeployPayload) componentEventPayload()           {}
func (ContainerDeployedPayload) componentEventPayload()       {}
func (ComponentConfigReloadPayload) componentEventPayload()   {}
func (ComponentSecretsChangedPayload) componentEventPayload() {}
func (ComponentManualDeployPayload) componentEventPayload()   {}
func (PolicyDeniedPayload) componentEventPayload()            {}
func (AuditPayload) componentEventPayload()                   {}
func (EdgeDrainPayload) componentEventPayload()               {}

func required(values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return errors.New("required payload field is empty")
		}
	}
	return nil
}
func (p RegistryImagePushedPayload) Validate() error {
	if err := required(p.Repository, p.Reference, p.Digest); err != nil {
		return err
	}
	if len(p.Manifest) > maxComponentEventManifestBytes {
		return fmt.Errorf("manifest exceeds %d bytes", maxComponentEventManifestBytes)
	}
	if len(p.Annotations) > maxComponentEventAnnotations {
		return fmt.Errorf("annotations exceed %d entries", maxComponentEventAnnotations)
	}
	total := 0
	for key, value := range p.Annotations {
		if strings.TrimSpace(key) == "" || len(key) > maxComponentEventAnnotationKey {
			return errors.New("annotation key is invalid")
		}
		if len(value) > maxComponentEventAnnotationVal {
			return errors.New("annotation value is too large")
		}
		total += len(key) + len(value)
		if total > maxComponentEventAnnotationAll {
			return errors.New("annotations are too large")
		}
	}
	return nil
}
func (p RuntimeStateChangedPayload) Validate() error { return required(p.ComponentID, p.State) }
func (p RuntimeDeployPayload) Validate() error {
	if p.Generation == 0 {
		return errors.New("generation is required")
	}
	return required(p.Domain, p.Image)
}
func (p ContainerDeployedPayload) Validate() error {
	if p.Generation == 0 {
		return errors.New("generation is required")
	}
	return required(p.Domain, p.Image, p.DeploymentID)
}
func (p ComponentConfigReloadPayload) Validate() error   { return required(p.Version) }
func (p ComponentSecretsChangedPayload) Validate() error { return required(p.Version) }
func (p ComponentManualDeployPayload) Validate() error {
	return required(p.Domain, p.Image, p.CorrelationID)
}
func (p PolicyDeniedPayload) Validate() error { return required(p.DecisionID, p.Action, p.Reason) }
func (p AuditPayload) Validate() error        { return required(p.Action, p.Subject, p.Outcome) }
func (p EdgeDrainPayload) Validate() error {
	if p.Generation == 0 {
		return errors.New("generation is required")
	}
	return required(p.Domain)
}
func (p RegistryImagePushedPayload) fingerprint() string {
	h := sha256.New()
	_, _ = h.Write([]byte(p.Repository + "\x00" + p.Reference + "\x00" + p.Digest + "\x00"))
	_, _ = h.Write(p.Manifest)
	keys := make([]string, 0, len(p.Annotations))
	for key := range p.Annotations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = h.Write([]byte("\x00" + key + "\x00" + p.Annotations[key]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (p RuntimeStateChangedPayload) fingerprint() string { return p.ComponentID + "\x00" + p.State }
func (p RuntimeDeployPayload) fingerprint() string {
	return p.Domain + "\x00" + p.Image + "\x00" + strconv.FormatUint(p.Generation, 10)
}
func (p ContainerDeployedPayload) fingerprint() string {
	return p.Domain + "\x00" + p.Image + "\x00" + p.DeploymentID + "\x00" + strconv.FormatUint(p.Generation, 10)
}
func (p ComponentConfigReloadPayload) fingerprint() string   { return p.Version }
func (p ComponentSecretsChangedPayload) fingerprint() string { return p.Version }
func (p ComponentManualDeployPayload) fingerprint() string {
	return p.Domain + "\x00" + p.Image + "\x00" + p.CorrelationID
}
func (p PolicyDeniedPayload) fingerprint() string {
	return p.DecisionID + "\x00" + p.Action + "\x00" + p.Reason
}
func (p AuditPayload) fingerprint() string { return p.Action + "\x00" + p.Subject + "\x00" + p.Outcome }
func (p EdgeDrainPayload) fingerprint() string {
	return p.Domain + "\x00" + strconv.FormatUint(p.Generation, 10)
}

// ComponentEventEnvelope is the transport-neutral, typed event envelope.
type ComponentEventEnvelope struct {
	ID                  string
	Type                ComponentEventType
	Origin              ComponentRole
	Timestamp           time.Time
	Generation          uint64
	IdempotencyKey      string
	Payload             ComponentEventPayload
	RetryCount          int
	AuditClassification ComponentEventAuditClassification
}

func (e ComponentEventEnvelope) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidComponentEvent)
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
	if e.Payload == nil {
		return fmt.Errorf("%w: typed payload is required", ErrInvalidComponentEvent)
	}
	if err := e.Payload.Validate(); err != nil {
		return fmt.Errorf("%w: invalid typed payload: %v", ErrInvalidComponentEvent, err)
	}
	if !payloadMatchesType(e.Type, e.Payload) {
		return fmt.Errorf("%w: payload does not match event type", ErrInvalidComponentEvent)
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

func payloadMatchesType(eventType ComponentEventType, payload ComponentEventPayload) bool {
	switch eventType {
	case ComponentEventTypeRegistryImagePushed:
		_, ok := payload.(RegistryImagePushedPayload)
		return ok
	case ComponentEventTypeRuntimeStateChanged:
		_, ok := payload.(RuntimeStateChangedPayload)
		return ok
	case ComponentEventTypeRuntimeDeploy:
		_, ok := payload.(RuntimeDeployPayload)
		return ok
	case ComponentEventTypeContainerDeployed:
		_, ok := payload.(ContainerDeployedPayload)
		return ok
	case ComponentEventTypeConfigReload:
		_, ok := payload.(ComponentConfigReloadPayload)
		return ok
	case ComponentEventTypeSecretsChanged:
		_, ok := payload.(ComponentSecretsChangedPayload)
		return ok
	case ComponentEventTypeManualDeploy:
		_, ok := payload.(ComponentManualDeployPayload)
		return ok
	case ComponentEventTypePolicyDenied:
		_, ok := payload.(PolicyDeniedPayload)
		return ok
	case ComponentEventTypeAudit:
		_, ok := payload.(AuditPayload)
		return ok
	case ComponentEventTypeEdgeDrain:
		_, ok := payload.(EdgeDrainPayload)
		return ok
	default:
		return false
	}
}

// DedupeKey is stable across retries and contains no secret material.
func (e ComponentEventEnvelope) DedupeKey() string {
	identity := strings.TrimSpace(e.IdempotencyKey)
	if identity == "" {
		identity = e.ID
	}
	h := sha256.New()
	for _, value := range []string{identity, string(e.Type), string(e.Origin), strconv.FormatUint(e.Generation, 10)} {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	if e.Payload != nil {
		_, _ = h.Write([]byte(e.Payload.fingerprint()))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isKnownComponentEventType(t ComponentEventType) bool {
	switch t {
	case ComponentEventTypeRegistryImagePushed, ComponentEventTypeRuntimeStateChanged, ComponentEventTypeRuntimeDeploy, ComponentEventTypeContainerDeployed, ComponentEventTypeConfigReload, ComponentEventTypeSecretsChanged, ComponentEventTypeManualDeploy, ComponentEventTypePolicyDenied, ComponentEventTypeAudit, ComponentEventTypeEdgeDrain:
		return true
	}
	return false
}
func (e ComponentEventEnvelope) requiresIdempotencyKey() bool {
	return e.AuditClassification == ComponentEventAuditWrite || e.AuditClassification == ComponentEventAuditSecurity || e.AuditClassification == ComponentEventAuditCritical || isCriticalComponentEventType(e.Type)
}
func isKnownComponentEventAuditClassification(c ComponentEventAuditClassification) bool {
	switch c {
	case ComponentEventAuditNone, ComponentEventAuditRead, ComponentEventAuditWrite, ComponentEventAuditSecurity, ComponentEventAuditCritical:
		return true
	}
	return false
}
func isCriticalComponentEventType(t ComponentEventType) bool {
	switch t {
	case ComponentEventTypeRegistryImagePushed, ComponentEventTypeRuntimeDeploy, ComponentEventTypeContainerDeployed, ComponentEventTypeManualDeploy, ComponentEventTypePolicyDenied, ComponentEventTypeEdgeDrain:
		return true
	}
	return false
}
