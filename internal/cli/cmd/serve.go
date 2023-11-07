// the serve command is used to start the gordon server
// Path: internal/cli/cmd/serve.go
package cmd

import (
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/spf13/cobra"
)

// NewServeCommand creates a new serve command
func NewServeCommand(a *server.App) *cobra.Command {
	defaultport := "1323"
	var port string

	// if the server is running in a docker container, the default port is 80
	if docker.IsRunningInContainer() {
		defaultport = "80"
	}

	// if no flags -p or --port are specified, the default port is used
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a new Gordon server instance",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// handler.StartServer will use the value of port, which will be the default
			// value if no flag is specified
			a.Config.Http.Port = port
			err := handler.StartServer(a, port)
			if err != nil {
				panic(err)
			}
		},
	}

	// Attach the flag to serveCmd and store its value in the variable port
	serveCmd.Flags().StringVarP(&port, "port", "p", defaultport, "port to listen on")
	return serveCmd
}
