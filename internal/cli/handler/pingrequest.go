package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
)

// Define a struct to match the JSON response structure
type PingResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

func PerformPingRequest(a *cli.App) (PingResponse, error) {
	var pingResp PingResponse

	pingPayload := common.PingPayload{Message: "ping"}
	reqPayload := common.RequestPayload{
		Type:    "ping",
		Payload: pingPayload,
	}

	resp, err := SendHTTPRequest(a, &reqPayload, "GET", "/ping")
	if err != nil {
		return pingResp, fmt.Errorf("failed to reach server: %w", err)
	}

	defer resp.Http.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PingResponse{}, fmt.Errorf("expected status code 200, got %d", resp.StatusCode)
	}

	// Unmarshal the JSON response into the PingResponse struct
	err = json.Unmarshal(resp.Body, &pingResp)
	if err != nil {
		return pingResp, err
	}

	return pingResp, nil
}
