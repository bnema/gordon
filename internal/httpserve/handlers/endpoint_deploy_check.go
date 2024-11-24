package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

func CheckDeployConflict(c echo.Context, a *server.App) error {
	targetDomain := c.QueryParam("domain")
	if targetDomain == "" {
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success:    false,
			Message:    "Target domain is required",
			StatusCode: http.StatusBadRequest,
		})
	}

	// Extract container name from the target domain
	cleanDomain := strings.TrimPrefix(strings.TrimPrefix(targetDomain, "https://"), "http://")
	containerName := strings.Split(cleanDomain, ".")[0]

	if containerName == "" {
		return sendJSONResponse(c, http.StatusBadRequest, DeployResponse{
			Success:    false,
			Message:    "Invalid target domain format",
			StatusCode: http.StatusBadRequest,
		})
	}

	// Check if container exists
	containerID := docker.GetContainerIDByName(containerName)
	if containerID == "" {
		return sendJSONResponse(c, http.StatusOK, DeployResponse{
			Success:    true,
			Message:    "No conflicts found",
			StatusCode: http.StatusOK,
		})
	}

	// Get container details
	state, err := docker.GetContainerState(containerID)
	if err != nil {
		log.Error("Failed to get container state", "error", err)
		state = "unknown"
	}

	ports, err := docker.GetContainerPorts(containerID)
	if err != nil {
		log.Error("Failed to get container ports", "error", err)
		ports = "unknown"
	}

	uptime, err := docker.GetContainerUptime(containerID)
	if err != nil {
		log.Error("Failed to get container uptime", "error", err)
		uptime = "unknown"
	}

	return sendJSONResponse(c, http.StatusConflict, DeployResponse{
		Success:       false,
		Message:       fmt.Sprintf("Container with name '%s' already exists", containerName),
		ContainerID:   containerID,
		ContainerName: containerName,
		State:         state,
		RunningTime:   uptime,
		Ports:         ports,
		StatusCode:    http.StatusConflict,
	})
}
