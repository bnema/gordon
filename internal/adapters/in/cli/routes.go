package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/domain"
)

// configPath for local operations. If empty, config is auto-discovered
// from standard locations (/etc/gordon/gordon.toml, ~/.config/gordon/gordon.toml, ./gordon.toml).
var configPath string

// truncateImage shortens long image references for display.
// For digests (image@sha256:...), shows first 12 chars of digest.
// For tags, truncates to maxLen with ellipsis if needed.
func truncateImage(image string, maxLen int) string {
	// Handle digest references: image@sha256:abc123...
	if maxLen <= 0 {
		return ""
	}

	if idx := strings.Index(image, "@sha256:"); idx != -1 {
		name := image[:idx]
		digest := image[idx+8:] // Skip "@sha256:"
		if len(digest) > 12 {
			digest = digest[:12]
		}
		short := fmt.Sprintf("%s@sha256:%s", name, digest)
		if len(short) <= maxLen {
			return short
		}
		// Truncate from the end to maintain valid reference format
		if maxLen <= 3 {
			return short[:maxLen]
		}
		return short[:maxLen-3] + "..."
	}

	// Regular tag: truncate if needed
	if len(image) <= maxLen {
		return image
	}
	if maxLen <= 3 {
		return image[:maxLen]
	}
	return image[:maxLen-3] + "..."
}

const networkPrefix = "gordon-"

// httpHealthToStatus maps an HTTP health probe result to a components.Status.
func httpHealthToStatus(health *remote.RouteHealth) components.Status {
	if health == nil {
		return components.StatusUnknown
	}
	if health.HTTPStatus == 0 {
		if health.Error != "" {
			return components.StatusError
		}
		return components.StatusUnknown
	}
	if health.HTTPStatus >= 200 && health.HTTPStatus < 400 {
		return components.StatusSuccess
	}
	return components.StatusError
}

// stripNetworkPrefix removes the "gordon-" prefix from a network name for display.
func stripNetworkPrefix(network string) string {
	return strings.TrimPrefix(network, networkPrefix)
}

// networkGroup holds routes that share a network.
type networkGroup struct {
	name   string
	routes []remote.RouteInfo
}

// groupRoutesByNetwork separates routes into network groups (2+ routes sharing a network)
// and solo routes. Both are sorted alphabetically by domain.
func groupRoutesByNetwork(routes []remote.RouteInfo) ([]networkGroup, []remote.RouteInfo) {
	byNetwork := make(map[string][]remote.RouteInfo)
	var networkOrder []string
	for _, route := range routes {
		if _, seen := byNetwork[route.Network]; !seen {
			networkOrder = append(networkOrder, route.Network)
		}
		byNetwork[route.Network] = append(byNetwork[route.Network], route)
	}

	var groups []networkGroup
	var solo []remote.RouteInfo

	for _, net := range networkOrder {
		members := byNetwork[net]
		if len(members) >= 2 {
			sort.Slice(members, func(i, j int) bool {
				return members[i].Domain < members[j].Domain
			})
			groups = append(groups, networkGroup{
				name:   stripNetworkPrefix(net),
				routes: members,
			})
		} else {
			solo = append(solo, members[0])
		}
	}

	sort.Slice(solo, func(i, j int) bool {
		return solo[i].Domain < solo[j].Domain
	})

	return groups, solo
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

	// "status" is an alias for "list"
	statusCmd := newRoutesListCmd()
	statusCmd.Use = "status"
	statusCmd.Short = "Show status of all routes (alias for list)"
	cmd.AddCommand(statusCmd)

	cmd.AddCommand(newRoutesShowCmd())
	cmd.AddCommand(newRoutesAddCmd())
	cmd.AddCommand(newRoutesRemoveCmd())

	return cmd
}

// newRoutesListCmd creates the routes list command.
func newRoutesListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			client, isRemote := GetRemoteClient()
			if isRemote {
				return runRoutesListRemote(ctx, client, jsonOut, cmd.OutOrStdout())
			}
			return runRoutesListLocal(ctx, configPath, jsonOut, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func newRoutesShowCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "show <domain>",
		Short: "Show details for a single route",
		Long: `Display detailed information about a specific route including its image,
container status, and health.

Examples:
  gordon routes show app.mydomain.com
  gordon routes show app.mydomain.com --json
  gordon routes show app.mydomain.com --remote https://gordon.mydomain.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runRoutesShow(ctx, handle.plane, cmd.OutOrStdout(), args[0], jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func runRoutesShow(ctx context.Context, cp ControlPlane, out io.Writer, routeDomain string, jsonOut bool) error {
	route, err := cp.GetRoute(ctx, routeDomain)
	if err != nil {
		if errors.Is(err, domain.ErrRouteNotFound) {
			return fmt.Errorf("route %q not found", routeDomain)
		}
		return fmt.Errorf("failed to get route: %w", err)
	}

	health, _ := cp.GetHealth(ctx)
	var routeHealth *remote.RouteHealth
	if health != nil {
		routeHealth = health[routeDomain]
	}

	containerStatus := "unknown"
	httpStatus := 0
	if routeHealth != nil {
		containerStatus = routeHealth.ContainerStatus
		httpStatus = routeHealth.HTTPStatus
	}

	if jsonOut {
		payload := map[string]any{
			"domain":           route.Domain,
			"image":            route.Image,
			"container_status": containerStatus,
			"http_status":      httpStatus,
		}
		return writeJSON(out, payload)
	}

	if err := cliWriteLine(out, cliRenderTitle("Route: "+route.Domain)); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Domain:", route.Domain)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Image:", route.Image)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Container:", containerStatus)); err != nil {
		return err
	}
	if httpStatus > 0 {
		if err := cliWriteLine(out, cliRenderMeta("HTTP Status:", fmt.Sprintf("%d", httpStatus))); err != nil {
			return err
		}
	}

	return nil
}

// runRoutesListRemote lists routes from a remote Gordon instance.
func runRoutesListRemote(ctx context.Context, client *remote.Client, jsonOut bool, out io.Writer) error {
	routes, err := client.ListRoutesWithDetails(ctx)
	if err != nil {
		return fmt.Errorf("failed to list routes: %w", err)
	}

	health, _ := client.GetHealth(ctx)
	if health == nil {
		health = make(map[string]*remote.RouteHealth)
	}

	if jsonOut {
		return routesListJSON(out, routes, health)
	}

	if len(routes) == 0 {
		_, _ = fmt.Fprintln(out, styles.Theme.Muted.Render("No routes configured"))
		return nil
	}

	groups, solo := groupRoutesByNetwork(routes)

	type sortableItem struct {
		sortKey string
		group   *networkGroup
		route   *remote.RouteInfo
	}

	var items []sortableItem
	for i := range groups {
		items = append(items, sortableItem{
			sortKey: groups[i].routes[0].Domain,
			group:   &groups[i],
		})
	}
	for i := range solo {
		items = append(items, sortableItem{
			sortKey: solo[i].Domain,
			route:   &solo[i],
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].sortKey < items[j].sortKey
	})

	tree := components.NewTree()

	for _, item := range items {
		if item.group != nil {
			g := tree.AddGroup(item.group.name)
			for _, route := range item.group.routes {
				title := routeTitle(route, health)
				node := g.AddNode(title, route.Image)
				addAttachmentChildren(node, route)
			}
		} else {
			title := routeTitle(*item.route, health)
			node := tree.AddNode(title, item.route.Image)
			addAttachmentChildren(node, *item.route)
		}
	}

	if err := cliWriteLine(out, cliRenderTitle("Routes")); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	return cliWriteLine(out, tree.Render())
}

// routeTitle builds the pre-styled title line for a route node.
func routeTitle(route remote.RouteInfo, health map[string]*remote.RouteHealth) string {
	containerStatus := route.ContainerStatus
	if containerStatus == "" {
		if h := health[route.Domain]; h != nil {
			containerStatus = h.ContainerStatus
		} else {
			containerStatus = "unknown"
		}
	}

	httpIcon := components.StatusIcon(styles.IconHTTPStatus, httpHealthToStatus(health[route.Domain]))
	containerIcon := components.StatusIcon(styles.IconContainerStatus, components.ParseStatus(containerStatus))

	return httpIcon + " " + containerIcon + " " + route.Domain
}

// addAttachmentChildren adds attachment nodes as children of a route node.
func addAttachmentChildren(node *components.Node, route remote.RouteInfo) {
	for _, att := range route.Attachments {
		attStatus := att.Status
		if attStatus == "" {
			attStatus = "unknown"
		}
		attIcon := components.StatusIcon(styles.IconContainerStatus, components.ParseStatus(attStatus))
		node.AddChild(attIcon+" "+att.Name, att.Image)
	}
}

func routesListJSON(out io.Writer, routes []remote.RouteInfo, health map[string]*remote.RouteHealth) error {
	payload := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		routeHealth := health[route.Domain]
		containerStatus := route.ContainerStatus
		if containerStatus == "" {
			if routeHealth != nil {
				containerStatus = routeHealth.ContainerStatus
			} else {
				containerStatus = "unknown"
			}
		}

		httpStatus := 0
		if routeHealth != nil {
			httpStatus = routeHealth.HTTPStatus
		}

		attachments := make([]map[string]string, 0, len(route.Attachments))
		for _, attachment := range route.Attachments {
			attachments = append(attachments, map[string]string{
				"name":   attachment.Name,
				"image":  attachment.Image,
				"status": attachment.Status,
			})
		}

		payload = append(payload, map[string]any{
			"domain":           route.Domain,
			"image":            route.Image,
			"container_status": containerStatus,
			"network":          route.Network,
			"http_status":      httpStatus,
			"attachments":      attachments,
		})
	}

	return writeJSON(out, payload)
}

// runRoutesListLocal lists routes from local configuration.
func runRoutesListLocal(ctx context.Context, cfgPath string, jsonOut bool, out io.Writer) error {
	local, err := GetLocalServices(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to initialize local services: %w", err)
	}

	routes := local.GetConfigService().GetRoutes(ctx)

	if jsonOut {
		payload := make([]map[string]string, 0, len(routes))
		for _, route := range routes {
			payload = append(payload, map[string]string{
				"domain": route.Domain,
				"image":  route.Image,
			})
		}
		return writeJSON(out, payload)
	}

	if len(routes) == 0 {
		_, _ = fmt.Fprintln(out, styles.Theme.Muted.Render("No routes configured"))
		return nil
	}

	// Build table rows (no health info in local mode)
	const imageColWidth = 45
	rows := make([][]string, len(routes))
	for i, route := range routes {
		displayImage := truncateImage(route.Image, imageColWidth)
		rows[i] = []string{route.Domain, displayImage}
	}

	// Render table
	table := components.NewTable(
		components.WithColumns([]components.TableColumn{
			{Title: "Domain", Width: 30},
			{Title: "Image", Width: imageColWidth},
		}),
		components.WithRows(rows),
	)

	_, _ = fmt.Fprintln(out, styles.Theme.Title.Render("Routes (local)"))
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, table.View())

	return nil
}

// newRoutesAddCmd creates the routes add command.
func newRoutesAddCmd() *cobra.Command {
	var image string

	cmd := &cobra.Command{
		Use:   "add <domain> <image>",
		Short: "Create or update a route",
		Long: `Create or update a route mapping a domain to a container image.

If the route already exists with the same image, this is a no-op.
If it exists with a different image, the image is updated.

Examples:
  gordon routes add app.mydomain.com myapp:latest
  gordon --remote https://gordon.mydomain.com routes add api.mydomain.com api:v2`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			routeDomain := args[0]

			// Resolve image from positional argument or flag
			hasPositionalImage := len(args) > 1
			hasFlagImage := image != ""

			// Check for conflicts: both positional and flag provided
			if hasPositionalImage && hasFlagImage {
				return fmt.Errorf("error: cannot use both positional image argument and --image flag")
			}

			// Check that image is provided via either method
			if !hasPositionalImage && !hasFlagImage {
				return fmt.Errorf("error: image is required (use positional argument or --image flag)")
			}

			if hasPositionalImage {
				image = args[1]
			}

			route := domain.Route{
				Domain: routeDomain,
				Image:  image,
			}

			client, isRemote := GetRemoteClient()
			if isRemote {
				if err := client.AddRoute(ctx, route); err != nil {
					return fmt.Errorf("failed to add route: %w", err)
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if err := local.GetConfigService().AddRoute(ctx, route); err != nil {
					return fmt.Errorf("failed to add route: %w", err)
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Route configured: %s -> %s", routeDomain, image)))
			return nil
		},
	}

	cmd.Flags().StringVarP(&image, "image", "i", "", "Container image")

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

			client, isRemote := GetRemoteClient()
			if isRemote {
				if err := client.RemoveRoute(ctx, routeDomain); err != nil {
					return fmt.Errorf("failed to remove route: %w", err)
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if err := local.GetConfigService().RemoveRoute(ctx, routeDomain); err != nil {
					return fmt.Errorf("failed to remove route: %w", err)
				}
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

			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			status, err := handle.plane.GetStatus(ctx)
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
