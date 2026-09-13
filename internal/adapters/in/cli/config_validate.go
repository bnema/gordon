package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/app"
)

type configDiagnostic struct {
	Code    string `json:"code"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

type configValidationResult struct {
	Valid       bool               `json:"valid"`
	Diagnostics []configDiagnostic `json:"diagnostics"`
	Scope       string             `json:"scope"`
}

func newConfigValidateCmd() *cobra.Command {
	var file string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Statically validate a local configuration file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if remoteFlag != "" {
				return fmt.Errorf("config validate is local-only; --remote is not supported")
			}
			return runConfigValidate(cmd.OutOrStdout(), file, jsonOut)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Local candidate configuration file (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func runConfigValidate(out io.Writer, file string, jsonOut bool) error {
	result := configValidationResult{Valid: true, Diagnostics: []configDiagnostic{}, Scope: "static"}
	if err := app.ValidateConfigFile(file); err != nil {
		result.Valid = false
		result.Diagnostics = []configDiagnostic{{Code: "config-invalid", Message: "configuration failed static validation"}}
		if jsonOut {
			if writeErr := writeJSON(out, result); writeErr != nil {
				return writeErr
			}
		}
		return fmt.Errorf("configuration failed static validation: %w", err)
	}
	if jsonOut {
		return writeJSON(out, result)
	}
	return cliWriteLine(out, cliRenderSuccess("Configuration is statically valid; runtime, ACTIVE-state, secret, pull, and listener checks were not performed."))
}
