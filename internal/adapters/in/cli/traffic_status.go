package cli

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
)

func newTrafficStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show remote traffic entrypoint, router, and counter status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrafficStatus(cmd.Context(), cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	return cmd
}

func runTrafficStatus(ctx context.Context, out io.Writer, jsonOut bool) error {
	client, isRemote, err := GetRemoteClient()
	if err != nil {
		return fmt.Errorf("create remote client: %w", err)
	}
	if !isRemote {
		return fmt.Errorf("traffic status currently requires --remote")
	}
	status, err := client.GetTrafficStatus(ctx)
	if err != nil {
		return fmt.Errorf("fetch traffic status: %w", err)
	}
	if err := renderTrafficStatus(out, status, jsonOut); err != nil {
		return fmt.Errorf("render traffic status: %w", err)
	}
	return nil
}

func renderTrafficStatus(out io.Writer, status *dto.TrafficStatusResponse, jsonOut bool) error {
	if jsonOut {
		return writeJSON(out, status)
	}
	if err := cliWriteLine(out, cliRenderTitle("Traffic Status")); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Reload", status.LastReloadStatus)); err != nil {
		return err
	}
	if status.LastReloadError != "" {
		if err := cliWriteLine(out, cliRenderWarning("Last reload error: "+status.LastReloadError)); err != nil {
			return err
		}
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, "Entrypoints:"); err != nil {
		return err
	}
	if len(status.EntryPoints) == 0 {
		if err := cliWriteLine(out, cliRenderMuted("  none")); err != nil {
			return err
		}
	} else {
		for _, entry := range status.EntryPoints {
			if err := cliWritef(out, "  %s  %s  %s  active=%t tcp=%d udp=%d accepted=%d refused=%d errors=%d\n",
				entry.Name, entry.Protocol, entry.Address, entry.Active, entry.ActiveTCPConnections, entry.ActiveUDPSessions,
				entry.TotalAccepted, entry.TotalRefused, entry.TotalErrors); err != nil {
				return err
			}
		}
	}
	if err := renderTrafficRouters(out, status.Routers); err != nil {
		return err
	}
	if err := renderTrafficServices(out, status.Services); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	return cliWritef(out, "Totals: tcp=%d udp=%d accepted=%d refused=%d errors=%d bytes_in=%d bytes_out=%d\n",
		status.Counters.ActiveTCPConnections, status.Counters.ActiveUDPSessions, status.Counters.TotalAccepted,
		status.Counters.TotalRefused, status.Counters.TotalErrors, status.Counters.BytesIn, status.Counters.BytesOut)
}

func renderTrafficRouters(out io.Writer, routers []dto.TrafficRouterStatus) error {
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, "Routers:"); err != nil {
		return err
	}
	if len(routers) == 0 {
		return cliWriteLine(out, cliRenderMuted("  none"))
	}
	for _, router := range routers {
		rule := router.Rule.Host
		if rule == "" {
			rule = router.Rule.SNI
		}
		if err := cliWritef(out, "  %s  %s  entrypoint=%s rule=%s service=%s active=%t\n",
			router.Name, router.Protocol, router.EntryPoint, rule, router.Service, router.Active); err != nil {
			return err
		}
	}
	return nil
}

func renderTrafficServices(out io.Writer, services []dto.TrafficServiceStatus) error {
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, "Services:"); err != nil {
		return err
	}
	if len(services) == 0 {
		return cliWriteLine(out, cliRenderMuted("  none"))
	}
	for _, service := range services {
		if err := cliWritef(out, "  %s  active=%t\n", service.Name, service.Active); err != nil {
			return err
		}
		for _, backend := range service.Backends {
			addr := net.JoinHostPort(backend.Host, fmt.Sprint(backend.Port))
			if err := cliWritef(out, "    %s  %s://%s active=%t\n",
				backend.Name, backend.Protocol, addr, backend.Active); err != nil {
				return err
			}
		}
	}
	return nil
}
