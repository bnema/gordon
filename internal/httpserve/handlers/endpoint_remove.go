package handlers

import (
	"net/http"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/labstack/echo/v4"
)

// PostContainerRemove handles the container remove request
func PostContainerRemove(c echo.Context, a *server.App) error {
	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload: "+err.Error())
	}

	if payload.Type != "remove" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload type")
	}

	removePayload, ok := payload.Payload.(common.RemovePayload)
	if !ok {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload structure")
	}

	if removePayload.ContainerID == "" {
		return sendJSONResponse(c, http.StatusBadRequest, "Invalid container ID")
	}

	err := docker.RemoveContainer(removePayload.ContainerID)
	if err != nil {
		return sendJSONResponse(c, http.StatusInternalServerError, RemoveResponse{
			Success: false,
			Message: err.Error(),
		})
	}

	return sendJSONResponse(c, http.StatusOK, RemoveResponse{
		Success: true,
		Message: "Container removed successfully",
	})
}
