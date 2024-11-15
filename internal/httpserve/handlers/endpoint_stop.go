package handlers

import (
	"net/http"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/labstack/echo/v4"
)

// PostContainerStop handles the container stop request
func PostContainerStop(c echo.Context, a *server.App) error {
    payload := new(common.RequestPayload)
    if err := c.Bind(payload); err != nil {
        return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload: "+err.Error())
    }

    if payload.Type != "stop" {
        return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload type")
    }

    stopPayload, ok := payload.Payload.(common.StopPayload)
    if !ok {
        return sendJSONResponse(c, http.StatusBadRequest, "Invalid payload structure")
    }

    if stopPayload.ContainerID == "" {
        return sendJSONResponse(c, http.StatusBadRequest, "Invalid container ID")
    }

    err := docker.StopContainer(stopPayload.ContainerID)
    if err != nil {
        return sendJSONResponse(c, http.StatusInternalServerError, StopResponse{
            Success: false,
            Message: err.Error(),
        })
    }

    return sendJSONResponse(c, http.StatusOK, StopResponse{
        Success: true,
        Message: "Container stopped successfully",
    })
}
