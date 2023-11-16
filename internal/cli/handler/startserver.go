package handler

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/bnema/gordon/internal/appserver"
	"github.com/bnema/gordon/internal/httpserve"
	"github.com/labstack/echo/v4"
)

// execute cmd/srv/main.go main function

func StartServer(a *appserver.App, port string) error {
	_, err := appserver.InitializeDB(a)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}

	// Start the session cleaner cron job
	a.StartSessionCleaner()

	_, err = a.HandleNewTokenInitialization()
	if err != nil {
		log.Print(err)
	}

	// Setup a channel to capture termination signals
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		log.Println("Received signal:", sig)
		os.Exit(0)
	}()

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e = httpserve.RegisterRoutes(e, a)

	log.Println("Starting server on port", a.Config.Http.Port)
	if err := e.Start(fmt.Sprintf(":%s", a.Config.Http.Port)); err != nil {
		log.Fatal(err)
	}

	return nil
}
