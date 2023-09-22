package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/labstack/echo/v4"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/httpserve"
	"github.com/bnema/gordon/pkg/utils/docker"
)

func cleanup(a *app.App, memDb *sql.DB) {
	if err := app.CloseAndBackupDB(a, memDb); err != nil {
		log.Fatal("Failed to close and backup database:", err)
	}
}

func main() {
	a := app.NewApp()

	// Initialize database
	memDb, err := app.InitializeDB(a)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}
	// Pass memDb to the app with the rest of the configs
	a.DB = memDb

	dockerClient := &docker.DockerClient{}
	err = dockerClient.InitializeClient(a.Config.NewDockerConfig())
	if err != nil {
		log.Printf("Error: %s", err)
	}
	// Setup a channel to capture termination signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		log.Println("Received signal:", sig)
		cleanup(a, memDb) // <- Call the cleanup function
		os.Exit(0)
	}()

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e = httpserve.RegisterRoutes(e, a)

	log.Println("Starting server on port", a.Config.Http.Port)
	if err := e.Start(fmt.Sprintf(":%d", a.Config.Http.Port)); err != nil {
		log.Fatal("Server error:", err)
	}
}
