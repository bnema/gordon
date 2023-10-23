package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/common"
	"github.com/labstack/echo/v4"
)

// Handle GET on /api/hello endpoint
func GetHello(c echo.Context, a *app.App) error {
	return c.JSON(http.StatusOK, "Hello, World!")
}

type InfoResponse struct {
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

func (info *InfoResponse) Populate(a *app.App) {
	info.Uptime = a.GetUptime()
	info.Version = a.Config.GetVersion()
}

// Handle GET on /api/ping endpoint
func GetInfos(c echo.Context, a *app.App) error {
	fmt.Println("GET /api/ping")
	body, _ := io.ReadAll(c.Request().Body)
	fmt.Println("Request Body:", string(body))
	c.Request().Body = io.NopCloser(bytes.NewBuffer(body)) // Reset the body

	payload := new(common.RequestPayload)
	if err := c.Bind(payload); err != nil {
		fmt.Println("Bind Error:", err)
		return c.JSON(http.StatusBadRequest, err.Error())
	}

	if payload.Type != "ping" {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}

	pingPayload, ok := payload.Payload.(common.PingPayload)
	if !ok {
		return c.JSON(http.StatusBadRequest, "Invalid payload type")
	}
	if pingPayload.Message != "ping" {
		return c.JSON(http.StatusBadRequest, "Invalid payload data")
	}

	// Prepare and populate the information
	info := &InfoResponse{}
	info.Populate(a)

	return c.JSON(http.StatusOK, info)

}
