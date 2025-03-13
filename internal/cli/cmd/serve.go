// the serve command is used to start the gordon server
// Path: internal/cli/cmd/serve.go
package cmd

import (
	"fmt"

	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/spf13/cobra"
)

// NewServeCommand creates a new serve command
func NewServeCommand(a *server.App) *cobra.Command {
	defaultport := "1323"
	var port string

	// if the server is running in a docker container, the default port is 8080
	// (changed from 80 to avoid conflict with the reverse proxy)
	if docker.IsRunningInContainer() {
		defaultport = "8080"
	}

	// if no flags -p or --port are specified, the default port is used
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a new Gordon server instance",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Initialize Docker client here, before starting the server
			if err := common.DockerInit(&a.Config.ContainerEngine); err != nil {
				fmt.Printf("Warning: %s\n", err)
				fmt.Println("Continuing with Docker/Podman functionality disabled")
			}

			// handler.StartServer will use the value of port, which will be the default
			// value if no flag is specified
			a.Config.Http.Port = port
			err := handler.StartServer(a, port)
			if err != nil {
				logger.Error("Server error", "error", err)
				// Ensure database is closed on error
				if err := a.Shutdown(); err != nil {
					logger.Error("Error during shutdown after server error", "error", err)
				}
				panic(err)
			}
		},
	}

	// Attach the flag to serveCmd and store its value in the variable port
	serveCmd.Flags().StringVarP(&port, "port", "p", defaultport, "port to listen on")
	return serveCmd
}
