package common

import (
	"encoding/json"
	"fmt"
	"io"
)

// RequestPayload represents the structure of incoming requests
type RequestPayload struct {
	Type    string  `json:"type"`
	Payload Payload `json:"payload"`
}

// Payload is an interface for different types of payloads
type Payload interface {
	GetType() string
}

// DeployResponse represents the response structure for deployment requests
type DeployResponse struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	StatusCode    int    `json:"status_code,omitempty"`
	Domain        string `json:"domain,omitempty"`
	ContainerID   string `json:"container_id,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	State         string `json:"state,omitempty"`
	RunningTime   string `json:"running_time,omitempty"`
	Ports         string `json:"ports,omitempty"`
}

// PushResponse represents the response structure for push requests
type PushResponse struct {
	Success            bool   `json:"success"`
	Message            string `json:"message"`
	CreateContainerURL string `json:"create_container_url,omitempty"`
	ImageID            string `json:"image_id,omitempty"`
}

type StopResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type RemoveResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type StartResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// DeployPayload represents the payload for deployment requests
type DeployPayload struct {
	Port         string `json:"port"`
	TargetDomain string `json:"targetdomain"`
	ImageName    string `json:"imagename"`
	ImageID      string `json:"imageid"`
	Data         io.ReadCloser
}

// PushPayload represents the payload for push requests
type PushPayload struct {
	ImageName string `json:"imagename"`
	ImageID   string `json:"imageid"`
	Data      io.Reader
}

type ChunkMetadata struct {
	ChunkNumber int    `json:"chunk_number"`
	TotalChunks int    `json:"total_chunks"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalSize   int64  `json:"total_size"`
	ImageName   string `json:"image_name"`
	TransferID  string `json:"transfer_id"`
}

type ConflictAction struct {
	Action string `json:"action"` // "stop", "remove", or "cancel"
	Force  bool   `json:"force"`
}

type DeviceFlowResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// UnmarshalJSON custom unmarshaler for RequestPayload
func (p *RequestPayload) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	err := json.Unmarshal(data, &raw)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(raw["type"], &p.Type); err != nil {
		return err
	}

	// Unmarshal the payload based on the type
	switch p.Type {
	case "ping":
		var payload PingPayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	case "deploy":
		var payload DeployPayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	case "push":
		var payload PushPayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	case "stop":
		var payload StopPayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	case "start":
		var payload StartPayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	case "remove":
		var payload RemovePayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	default:
		return fmt.Errorf("invalid type: %s", p.Type)
	}

	return nil
}

// PingPayload represents the payload for ping requests
type PingPayload struct {
	Message string `json:"message"`
}

// GetType returns the type of the PingPayload
func (p PingPayload) GetType() string {
	return "ping"
}

// GetType returns the type of the DeployPayload
func (p DeployPayload) GetType() string {
	return "deploy"
}

// GetType returns the type of the PushPayload
func (p PushPayload) GetType() string {
	return "push"
}

// NewPingPayload creates a new PingPayload from a map of data
func NewPingPayload(data map[string]interface{}) (PingPayload, error) {
	message, ok := data["message"].(string)
	if !ok {
		return PingPayload{}, fmt.Errorf("invalid data for message")
	}
	return PingPayload{Message: message}, nil
}

// StopPayload represents the payload for stop requests
type StopPayload struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
}

// GetType returns the type of the StopPayload
func (p StopPayload) GetType() string {
	return "stop"
}

// StartPayload represents the payload for start requests
type StartPayload struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
}

// GetType returns the type of the StartPayload
func (p StartPayload) GetType() string {
	return "start"
}

// RemovePayload represents the payload for remove requests
type RemovePayload struct {
	ContainerID string `json:"container_id"`
}

// GetType returns the type of the RemovePayload
func (p RemovePayload) GetType() string {
	return "remove"
}
