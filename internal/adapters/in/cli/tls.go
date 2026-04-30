package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// tlsResolveControlPlane allows tests to override control-plane resolution.
var tlsResolveControlPlane = resolveControlPlane

func newTLSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tls",
		Short: "Inspect TLS certificate status",
	}

	cmd.AddCommand(newTLSStatusCmd())

	return cmd
}

func newTLSStatusCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show public TLS certificate status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			handle, err := tlsResolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runTLSStatusCmd(cmd.Context(), handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func runTLSStatusCmd(ctx context.Context, cp ControlPlane, out io.Writer, jsonOut bool) error {
	status, err := cp.GetTLSStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get TLS status: %w", err)
	}
	if status == nil {
		return fmt.Errorf("failed to get TLS status: empty response")
	}

	if jsonOut {
		return writeJSON(out, status)
	}

	return renderTLSStatusText(out, status)
}

func renderTLSStatusText(out io.Writer, s *dto.TLSStatusResponse) error {
	if err := cliWriteLine(out, cliRenderTitle("Public TLS / ACME Status")); err != nil {
		return err
	}

	if err := renderTLSModeSection(out, s); err != nil {
		return err
	}

	if err := renderTLSCertificatesSection(out, s); err != nil {
		return err
	}

	if err := renderTLSRouteCoverageSection(out, s); err != nil {
		return err
	}

	return renderTLSErrorsSection(out, s)
}

func renderTLSModeSection(out io.Writer, s *dto.TLSStatusResponse) error {
	acmeStatus := "disabled"
	if s.ACMEEnabled {
		acmeStatus = "enabled"
	}
	if err := cliWriteLine(out, cliRenderMeta("ACME:", acmeStatus)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Configured Mode:", s.ConfiguredMode)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Effective Mode:", s.EffectiveMode)); err != nil {
		return err
	}
	if s.SelectionReason != "" {
		if err := cliWriteLine(out, cliRenderMeta("Reason:", s.SelectionReason)); err != nil {
			return err
		}
	}
	return cliWriteLine(out, cliRenderMeta("Token Source:", s.TokenSource))
}

func renderTLSCertificatesSection(out io.Writer, s *dto.TLSStatusResponse) error {
	if len(s.Certificates) == 0 {
		return nil
	}

	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderTitle("Certificates")); err != nil {
		return err
	}

	for _, cert := range s.Certificates {
		if err := cliWritef(out, "  %s\n", cliRenderMeta("ID:", cert.ID)); err != nil {
			return err
		}
		if err := cliWriteLine(out, cliRenderMeta("  Names:", strings.Join(cert.Names, ", "))); err != nil {
			return err
		}
		if err := cliWriteLine(out, cliRenderMeta("  Status:", cert.Status)); err != nil {
			return err
		}
		if !cert.NotAfter.IsZero() {
			if err := cliWriteLine(out, cliRenderMeta("  Not After:", cert.NotAfter.Format(time.DateTime))); err != nil {
				return err
			}
		}
		if cert.LastError != "" {
			if err := cliWriteLine(out, cliRenderMeta("  Last Error:", cert.LastError)); err != nil {
				return err
			}
		}
	}

	return nil
}

func renderTLSRouteCoverageSection(out io.Writer, s *dto.TLSStatusResponse) error {
	if len(s.Routes) == 0 {
		return nil
	}

	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderTitle("Route Coverage")); err != nil {
		return err
	}

	for _, route := range s.Routes {
		covered := "no"
		if route.Covered {
			covered = "yes"
		}
		if err := cliWritef(out, "  %s  covered=%s", route.Domain, covered); err != nil {
			return err
		}
		if route.CoveredBy != "" {
			if err := cliWritef(out, "  covered_by=%s", route.CoveredBy); err != nil {
				return err
			}
		}
		if route.Error != "" {
			if err := cliWritef(out, "  error=%s", route.Error); err != nil {
				return err
			}
		}
		if err := cliWriteLine(out, ""); err != nil {
			return err
		}
	}

	return nil
}

func renderTLSErrorsSection(out io.Writer, s *dto.TLSStatusResponse) error {
	if len(s.Errors) == 0 {
		return nil
	}

	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderTitle("Errors")); err != nil {
		return err
	}

	for _, e := range s.Errors {
		if err := cliWriteLine(out, "  "+e); err != nil {
			return err
		}
	}

	return nil
}
