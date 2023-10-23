// the serve command is used to start the gordon server
// Path: internal/cli/cmd/serve.go
package cmd

import (
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/spf13/cobra"
)

// execute cmd/srv/main.go main function

func NewServeCommand(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the gordon server",
		Run: func(cmd *cobra.Command, args []string) {
			handler.StartServer(a)
		},
	}
}
