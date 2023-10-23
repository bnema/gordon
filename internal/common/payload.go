package common

import (
	"errors"
)

// RequestPayload holds the type and payload of a client request
type RequestPayload struct {
	Type    string  `json:"type"`
	Payload Payload `json:"payload"`
}

// Request is the client request type
type Request struct {
	Token   string         `json:"token"`
	Request RequestPayload `json:"request"`
}

// Payload interface exposes the type and allows for extensible payloads
type Payload interface {
	GetType() string
}

// HelloWorldPayload for testing
type HelloWorldPayload struct {
	Message string `json:"message"`
}

// GetType implementation for HelloWorldPayload
func (p HelloWorldPayload) GetType() string {
	return "hello"
}

// PushPayload for pushing container images
type PushPayload struct {
	Image string `json:"image"`
	Tag   string `json:"tag"`
}

// GetType implementation for PushPayload
func (p PushPayload) GetType() string {
	return "push"
}

// CreatePayload is a factory function to create a Payload based on the type
func CreatePayload(payloadType string, data map[string]interface{}) (Payload, error) {
	switch payloadType {
	case "hello":
		message, ok := data["message"].(string)
		if !ok {
			return nil, errors.New("invalid data for HelloWorldPayload")
		}
		return HelloWorldPayload{Message: message}, nil
	case "push":
		// TODO: implement
	default:
		return nil, errors.New("unknown payload type")
	}

	return nil, nil
}
