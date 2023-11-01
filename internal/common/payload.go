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

// What do we need inside the push payload :
// the ports as a string (80:80/tcp, 443:443/tcp)
// the image+tag as a string (nginx:latest)
// the .tar container image as a byte array

type PushPayload struct {
	Ports        string `json:"ports"`
	TargetDomain string `json:"targetdomain"`
	ImageName    string `json:"imagename"`
	ImageID      string `json:"imageid"`
	Data         io.ReadCloser
}
