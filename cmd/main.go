package main

import (
	"fmt"
	"log/slog"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/httpserve"
	"github.com/labstack/echo/v4"
)

func main() {
	a := app.NewApp()
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e = httpserve.RegisterRoutes(e, a)
	slog.Info("Starting server", "port", a.HttpPort)
	slog.Error("Server error", e.Start(fmt.Sprintf(":%d", a.HttpPort)))
}
