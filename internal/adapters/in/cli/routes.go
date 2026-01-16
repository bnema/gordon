package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gordon/internal/adapters/in/cli/remote"
	"gordon/internal/adapters/in/cli/ui/components"
	"gordon/internal/adapters/in/cli/ui/styles"
	"gordon/internal/domain"

	"github.com/spf13/cobra"
)

// truncateImage shortens long image references for display.
// For digests (image@sha256:...), shows first 12 chars of digest.
// For tags, truncates to maxLen with ellipsis if needed.
func truncateImage(image string, maxLen int) string {
	// Handle digest references: image@sha256:abc123...
	if idx := strings.Index(image, "@sha256:"); idx != -1 {
		name := image[:idx]
		digest := image[idx+8:] // Skip "@sha256:"
		if len(digest) > 12 {
			digest = digest[:12]
		}
		short := fmt.Sprintf("%s@sha256:%s…", name, digest)
		if len(short) > maxLen {
			// Truncate name if still too long
			available := maxLen - len("@sha256:") - 12 - 1 // 1 for ellipsis
			if available > 3 {
				name = name[:available-1] + "…"
			}
			short = fmt.Sprintf("%s@sha256:%s…", name, digest)
		}
		return short
	}

	// Regular tag: truncate if needed
	if len(image) > maxLen {
		return image[:maxLen-1] + "…"
	}
	return image
}

// newRoutesCmd creates the routes command group.
func newRoutesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "Manage routes",
		Long: `Manage Gordon routes. Routes map domains to container images.

When targeting a remote Gordon instance (via --remote flag or GORDON_REMOTE env var),
these commands operate on the remote server. Otherwise, they require access to
the local Gordon configuration.`,
	}

	cmd.AddCommand(newRoutesListCmd())
	cmd.AddCommand(newRoutesAddCmd())
	cmd.AddCommand(newRoutesRemoveCmd())

	return cmd
}

// formatHTTPStatus formats the HTTP status for display.
func formatHTTPStatus(health *remote.RouteHealth) string {
	if health == nil {
		return styles.Theme.Muted.Render("-")
	}
	if health.HTTPStatus == 0 {
		if health.Error != "" {
			return styles.Theme.BadgeError.Render("err")
		}
		return styles.Theme.Muted.Render("-")
	}
	status := fmt.Sprintf("%d", health.HTTPStatus)
	if health.ResponseTimeMs > 0 {
		status = fmt.Sprintf("%d (%dms)", health.HTTPStatus, health.ResponseTimeMs)
	}
	if health.HTTPStatus >= 200 && health.HTTPStatus < 400 {
		return styles.Theme.BadgeSuccess.Render(status)
	}
	return styles.Theme.BadgeError.Render(status)
}

// newRoutesListCmd creates the routes list command.
func newRoutesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("routes command requires --remote flag or GORDON_REMOTE env var"))
				fmt.Println(styles.Theme.Muted.Render("Local route management is not yet supported. Use the config file directly."))
				return nil
			}

			routes, err := client.ListRoutes(ctx)
			if err != nil {
				return fmt.Errorf("failed to list routes: %w", err)
			}

			if len(routes) == 0 {
				fmt.Println(styles.Theme.Muted.Render("No routes configured"))
				return nil
			}

			// Get health status for each route (includes container status and HTTP probe)
			health, _ := client.GetHealth(ctx)
			if health == nil {
				health = make(map[string]*remote.RouteHealth)
			}

			// Build table rows
			const imageColWidth = 35
			rows := make([][]string, len(routes))
			for i, route := range routes {
				routeHealth := health[route.Domain]

				// Container status column
				containerStatus := "unknown"
				if routeHealth != nil {
					containerStatus = routeHealth.ContainerStatus
				}
				containerBadge := components.ContainerStatusBadge(containerStatus)

				// HTTP status column
				httpStatus := formatHTTPStatus(routeHealth)

				displayImage := truncateImage(route.Image, imageColWidth)
				rows[i] = []string{route.Domain, displayImage, containerBadge, httpStatus}
			}

			// Render table
			table := components.NewTable(
				components.WithColumns([]components.TableColumn{
					{Title: "Domain", Width: 25},
					{Title: "Image", Width: imageColWidth},
					{Title: "Container", Width: 12},
					{Title: "HTTP", Width: 14},
				}),
				components.WithRows(rows),
			)

			fmt.Println(styles.Theme.Title.Render("Routes"))
			fmt.Println()
			fmt.Println(table.View())

			return nil
		},
	}
}

// newRoutesAddCmd creates the routes add command.
func newRoutesAddCmd() *cobra.Command {
	var image string

	cmd := &cobra.Command{
		Use:   "add <domain>",
		Short: "Add a new route",
		Long: `Add a new route mapping a domain to a container image.

Examples:
  gordon routes add app.mydomain.com --image myapp:latest
  gordon --remote https://gordon.mydomain.com routes add api.mydomain.com --image api:v2`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			routeDomain := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("routes command requires --remote flag or GORDON_REMOTE env var"))
				return nil
			}

			if image == "" {
				fmt.Println(styles.RenderError("--image flag is required"))
				return nil
			}

			route := domain.Route{
				Domain: routeDomain,
				Image:  image,
			}

			if err := client.AddRoute(ctx, route); err != nil {
				return fmt.Errorf("failed to add route: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Route added: %s -> %s", routeDomain, image)))
			return nil
		},
	}

	cmd.Flags().StringVarP(&image, "image", "i", "", "Container image (required)")
	_ = cmd.MarkFlagRequired("image")

	return cmd
}

// newRoutesRemoveCmd creates the routes remove command.
func newRoutesRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <domain>",
		Short: "Remove a route",
		Long: `Remove a route by its domain name.

Examples:
  gordon routes remove app.mydomain.com
  gordon routes remove app.mydomain.com --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			routeDomain := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("routes command requires --remote flag or GORDON_REMOTE env var"))
				return nil
			}

			// Confirm unless --force
			if !force {
				confirmed, err := components.RunConfirm(
					fmt.Sprintf("Remove route '%s'?", routeDomain),
					components.WithDescription("This will stop and remove the associated container."),
				)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println(styles.Theme.Muted.Render("Cancelled"))
					return nil
				}
			}

			if err := client.RemoveRoute(ctx, routeDomain); err != nil {
				return fmt.Errorf("failed to remove route: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Route removed: %s", routeDomain)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

// newStatusCmd creates the status command.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Gordon server status",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("status command requires --remote flag or GORDON_REMOTE env var"))
				os.Exit(1)
			}

			status, err := client.GetStatus(ctx)
			if err != nil {
				return fmt.Errorf("failed to get status: %w", err)
			}

			// Display status
			fmt.Println(styles.Theme.Title.Render("Gordon Status"))
			fmt.Println()

			fmt.Printf("%s %s\n", styles.Theme.Bold.Render("Domain:"), status.RegistryDomain)
			fmt.Printf("%s %d\n", styles.Theme.Bold.Render("Registry Port:"), status.RegistryPort)
			fmt.Printf("%s %d\n", styles.Theme.Bold.Render("Server Port:"), status.ServerPort)
			fmt.Printf("%s %d\n", styles.Theme.Bold.Render("Routes:"), status.Routes)
			fmt.Printf("%s %v\n", styles.Theme.Bold.Render("Auto-Route:"), status.AutoRoute)
			fmt.Printf("%s %v\n", styles.Theme.Bold.Render("Network Isolation:"), status.NetworkIsolation)

			if len(status.ContainerStatus) > 0 {
				fmt.Println()
				fmt.Println(styles.Theme.Bold.Render("Container Status:"))
				for domain, containerStatus := range status.ContainerStatus {
					badge := components.ContainerStatusBadge(containerStatus)
					fmt.Printf("  %s: %s\n", domain, badge)
				}
			}

			return nil
		},
	}
}
