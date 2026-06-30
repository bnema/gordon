package cli

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
)

var trafficResolveControlPlane = resolveControlPlane

func newTrafficStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show traffic entrypoint, router, and counter status",
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := trafficResolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runTrafficStatus(cmd.Context(), handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	return cmd
}

func runTrafficStatus(ctx context.Context, cp ControlPlane, out io.Writer, jsonOut bool) error {
	status, err := cp.GetTrafficStatus(ctx)
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
	if err := renderTrafficStatusHeader(out, status); err != nil {
		return err
	}
	if err := renderTrafficEntryPoints(out, status.EntryPoints); err != nil {
		return err
	}
	if err := renderTrafficRouters(out, status.Routers); err != nil {
		return err
	}
	if err := renderTrafficServices(out, status.Services); err != nil {
		return err
	}
	return renderTrafficTotals(out, status.Counters)
}

func renderTrafficStatusHeader(out io.Writer, status *dto.TrafficStatusResponse) error {
	if err := cliWriteLine(out, cliRenderTitle("Traffic Status")); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Reload", status.LastReloadStatus)); err != nil {
		return err
	}
	if status.LastReloadError == "" {
		return nil
	}
	return cliWriteLine(out, cliRenderWarning("Last reload error: "+status.LastReloadError))
}

func renderTrafficEntryPoints(out io.Writer, entries []dto.TrafficEntryPointStatus) error {
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, "Entrypoints:"); err != nil {
		return err
	}
	if len(entries) == 0 {
		return cliWriteLine(out, cliRenderMuted("  none"))
	}
	for _, entry := range entries {
		if err := renderTrafficEntryPoint(out, entry); err != nil {
			return err
		}
	}
	return nil
}

func renderTrafficEntryPoint(out io.Writer, entry dto.TrafficEntryPointStatus) error {
	if err := cliWritef(out, "  %s  %s  %s  active=%t tcp=%d udp=%d accepted=%d refused=%d errors=%d\n",
		entry.Name, entry.Protocol, entry.Address, entry.Active, entry.ActiveTCPConnections, entry.ActiveUDPSessions,
		entry.TotalAccepted, entry.TotalRefused, entry.TotalErrors); err != nil {
		return err
	}
	if hasSmartTCPCounters(entry.SmartTCP) {
		return renderSmartTCPCounters(out, "    smart_tcp", entry.SmartTCP)
	}
	return nil
}

func renderTrafficTotals(out io.Writer, counters dto.TrafficCounters) error {
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWritef(out, "Totals: tcp=%d udp=%d accepted=%d refused=%d errors=%d bytes_in=%d bytes_out=%d\n",
		counters.ActiveTCPConnections, counters.ActiveUDPSessions, counters.TotalAccepted,
		counters.TotalRefused, counters.TotalErrors, counters.BytesIn, counters.BytesOut); err != nil {
		return err
	}
	if hasSmartTCPCounters(counters.SmartTCP) {
		return renderSmartTCPCounters(out, "Smart TCP totals", counters.SmartTCP)
	}
	return nil
}

func hasSmartTCPCounters(c dto.SmartTCPCounters) bool {
	return c.HTTPAccepted != 0 || c.H2CAccepted != 0 || c.HTTPSFallbackAccepted != 0 || c.TLSPassthroughAccepted != 0 ||
		c.RawFallbackAccepted != 0 || c.EntrypointCIDRRefused != 0 || c.RawFallbackCIDRRefused != 0 || c.PROXYRefused != 0 ||
		c.UnknownNoFallbackRefused != 0 || c.MalformedRejected != 0 || c.SniffTimeout != 0 || c.ClientHelloTooLarge != 0
}

func renderSmartTCPCounters(out io.Writer, label string, c dto.SmartTCPCounters) error {
	return cliWritef(out, "%s: http_accepted=%d h2c_accepted=%d https_fallback_accepted=%d tls_passthrough_accepted=%d raw_fallback_accepted=%d entrypoint_cidr_refused=%d raw_fallback_cidr_refused=%d proxy_refused=%d unknown_no_fallback_refused=%d malformed_rejected=%d sniff_timeout=%d client_hello_too_large=%d\n",
		label, c.HTTPAccepted, c.H2CAccepted, c.HTTPSFallbackAccepted, c.TLSPassthroughAccepted, c.RawFallbackAccepted,
		c.EntrypointCIDRRefused, c.RawFallbackCIDRRefused, c.PROXYRefused, c.UnknownNoFallbackRefused, c.MalformedRejected,
		c.SniffTimeout, c.ClientHelloTooLarge)
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
