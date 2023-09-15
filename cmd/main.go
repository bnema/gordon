package main

import (
	"fmt"
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/httpserve"
)

func main() {
	a := app.NewApp()

	// Initialize database
	db, err := app.InitializeDB(a)
	if err != nil {
		slog.Error("Failed to load database", err)
	} else {
		slog.Info("Database loaded")
	}

	fmt.Println(db)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e = httpserve.RegisterRoutes(e, a)
	slog.Info("Starting server", "port", a.HttpPort)
	slog.Error("Server error", e.Start(fmt.Sprintf(":%d", a.HttpPort)))

}
