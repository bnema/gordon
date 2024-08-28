package handler

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bnema/gordon/internal/httpserve"
	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
	"github.com/labstack/echo/v4"
)

// execute cmd/srv/main.go main function

func StartServer(a *server.App, port string) error {
	_, err := server.InitializeDB(a)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}

	// Start the session cleaner cron job
	a.StartSessionCleaner()

	_, err = server.HandleNewTokenInitialization(a)
	if err != nil {
		log.Print(err)
	}

	// Setup a channel to capture termination signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		log.Info(fmt.Println("Received signal:", sig))
		os.Exit(0)
	}()

	e := echo.New()
	e.Server.ReadTimeout = 10 * time.Minute
	e.Server.WriteTimeout = 10 * time.Minute
	e.HideBanner = true
	e.HidePort = true
	e = httpserve.RegisterRoutes(e, a)

	log.Info(fmt.Sprintf("Starting server on port %s", port))
	if err := e.Start(fmt.Sprintf(":%s", a.Config.Http.Port)); err != nil {
		log.Fatal(err)
	}

	return nil
}
