package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
)

func newBootstrapCmd() *cobra.Command {
	var attachments []string
	var envPairs []string
	var attachmentEnvPairs []string
	var configPath string

	cmd := &cobra.Command{
		Use:   "bootstrap <domain> <image>",
		Short: "Create a route, attachments, and secrets together",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			req := dto.BootstrapRequest{
				Domain:        args[0],
				Image:         args[1],
				Attachments:   append([]string(nil), attachments...),
				Env:           map[string]string{},
				AttachmentEnv: map[string]map[string]string{},
			}

			for _, pair := range envPairs {
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid --env value %q (expected KEY=VALUE)", pair)
				}
				req.Env[parts[0]] = parts[1]
			}

			for _, pair := range attachmentEnvPairs {
				serviceAndKV := strings.SplitN(pair, ":", 2)
				if len(serviceAndKV) != 2 {
					return fmt.Errorf("invalid --attachment-env value %q (expected service:KEY=VALUE)", pair)
				}
				keyValue := strings.SplitN(serviceAndKV[1], "=", 2)
				if len(keyValue) != 2 {
					return fmt.Errorf("invalid --attachment-env value %q (expected service:KEY=VALUE)", pair)
				}
				service := serviceAndKV[0]
				if req.AttachmentEnv[service] == nil {
					req.AttachmentEnv[service] = map[string]string{}
				}
				req.AttachmentEnv[service][keyValue[0]] = keyValue[1]
			}

			if len(req.Env) == 0 {
				req.Env = nil
			}
			if len(req.AttachmentEnv) == 0 {
				req.AttachmentEnv = nil
			}

			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			resp, err := handle.plane.Bootstrap(ctx, req)
			if resp != nil {
				if writeErr := printBootstrapSummary(cmd, resp); writeErr != nil {
					return writeErr
				}
			}
			if err != nil {
				return fmt.Errorf("failed to bootstrap: %w", err)
			}
			out := cmd.OutOrStdout()
			if err := cliWriteLine(out, cliRenderSuccess("Bootstrap complete")); err != nil {
				return err
			}
			return cliWriteLine(out, fmt.Sprintf("Next: gordon push %s --build", req.Image))
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().StringArrayVar(&attachments, "attachment", nil, "Attachment image (repeatable)")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "Environment variable KEY=VALUE (repeatable)")
	cmd.Flags().StringArrayVar(&attachmentEnvPairs, "attachment-env", nil, "Attachment env service:KEY=VALUE (repeatable)")

	return cmd
}

func printBootstrapSummary(cmd *cobra.Command, resp *dto.BootstrapResponse) error {
	out := cmd.OutOrStdout()
	if err := cliWriteLine(out, cliRenderTitle("Bootstrap")); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Domain:", resp.Domain)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Image:", resp.Image)); err != nil {
		return err
	}
	for _, step := range resp.Steps {
		if err := cliWriteLine(out, fmt.Sprintf("- %s: %s", step.Name, step.Status)); err != nil {
			return err
		}
	}
	if len(resp.Warnings) > 0 {
		sorted := append([]string(nil), resp.Warnings...)
		sort.Strings(sorted)
		for _, warning := range sorted {
			if err := cliWriteLine(out, cliRenderWarning(warning)); err != nil {
				return err
			}
		}
	}
	return nil
}
