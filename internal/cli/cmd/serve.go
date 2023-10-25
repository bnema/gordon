// the serve command is used to start the gordon server
// Path: internal/cli/cmd/serve.go
package cmd

import (
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/server"
	"github.com/spf13/cobra"
)

// NewServeCommand creates a new serve command
func NewServeCommand(a *server.App) *cobra.Command {
	var port string
	defaultport := "1323"

	// if no flags -p or --port are specified, the default port is used
	serveCmd := &cobra.Command{
		Use:  "serve",
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// handler.StartServer will use the value of port, which will be the default
			// value if no flag is specified
			handler.StartServer(a, port)
		},
	}

	// Attach the flag to serveCmd and store its value in the variable port
	serveCmd.Flags().StringVarP(&port, "port", "p", defaultport, "Port to listen on")

	return serveCmd
}
