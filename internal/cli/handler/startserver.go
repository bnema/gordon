package handler

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/bnema/gordon/internal/httpserve"
	"github.com/bnema/gordon/internal/server"
	"github.com/labstack/echo/v4"
)

// execute cmd/srv/main.go main function

func StartServer(a *server.App, port string) error {
	_, err := server.InitializeDB(a)
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}

	_, err = server.HandleNewTokenInitialization(a)
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
