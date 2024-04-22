package common

import (
	"encoding/json"
	"fmt"

	"io"
)

type RequestPayload struct {
	Type    string  `json:"type"`
	Payload Payload `json:"payload"`
}

type Payload interface {
	GetType() string
}

func (p *RequestPayload) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	err := json.Unmarshal(data, &raw)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(raw["type"], &p.Type); err != nil {
		return err
	}

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
	default:
		return fmt.Errorf("invalid type: %s", p.Type)
	}

	return nil
}

type PingPayload struct {
	Message string `json:"message"`
}

func (p PingPayload) GetType() string {
	return "ping"
}

func (p DeployPayload) GetType() string {
	return "deploy"
}

func (p PushPayload) GetType() string {
	return "push"
}

func NewPingPayload(data map[string]interface{}) (PingPayload, error) {
	message, ok := data["message"].(string)
	if !ok {
		return PingPayload{}, fmt.Errorf("invalid data for message")
	}
	return PingPayload{Message: message}, nil
}

type DeployPayload struct {
	Ports        string `json:"ports"`
	TargetDomain string `json:"targetdomain"`
	ImageName    string `json:"imagename"`
	ImageID      string `json:"imageid"`
	Data         io.ReadCloser
}

type PushPayload struct {
	ImageName string `json:"imagename"`
	Data      io.ReadCloser
}
