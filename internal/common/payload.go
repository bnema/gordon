package common

import (
	"encoding/json"
	"fmt"
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
	case "push":
		var payload PushPayload
		if err := json.Unmarshal(raw["payload"], &payload); err != nil {
			return err
		}
		p.Payload = payload
	}
	return nil
}

type PingPayload struct {
	Message string `json:"message"`
}

func (p PingPayload) GetType() string {
	return "ping"
}

type PushPayload struct {
	Data []byte `json:"data"`
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
