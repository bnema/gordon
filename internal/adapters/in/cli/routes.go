package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

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

type routeListItem struct {
	Domain string `json:"domain"`
	Image  string `json:"image"`
}

type routeListSection struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	URL    string          `json:"url,omitempty"`
	Error  string          `json:"error,omitempty"`
	Routes []routeListItem `json:"routes,omitempty"`
}

type routeStatusAttachment struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
}

type routeStatusItem struct {
	Domain          string                  `json:"domain"`
	Image           string                  `json:"image"`
	ContainerID     string                  `json:"container_id,omitempty"`
	ContainerStatus string                  `json:"container_status"`
	HTTPStatus      int                     `json:"http_status"`
	HealthError     string                  `json:"health_error,omitempty"`
	Network         string                  `json:"network"`
	Attachments     []routeStatusAttachment `json:"attachments,omitempty"`
}

type routeStatusSection struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	URL    string            `json:"url,omitempty"`
	Error  string            `json:"error,omitempty"`
	Routes []routeStatusItem `json:"routes,omitempty"`
}

type routesListDeps struct {
	explicitRemote func() (*remote.ResolvedRemote, bool)
	loadLocal      func(context.Context, string) (routeListSection, error)
	listRemotes    func() (map[string]remote.RemoteEntry, string, error)
	loadRemote     func(context.Context, string, remote.RemoteEntry) (routeListSection, error)
}

type routesStatusDeps struct {
	explicitRemote func() (*remote.ResolvedRemote, bool)
	loadLocal      func(context.Context, string) (routeStatusSection, error)
	listRemotes    func() (map[string]remote.RemoteEntry, string, error)
	loadRemote     func(context.Context, string, remote.RemoteEntry) (routeStatusSection, error)
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
	cmd.AddCommand(newRoutesStatusCmd())

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
			sections, err := collectRoutesListSections(cmd.Context(), configPath, routesListDeps{})
			if err != nil {
				return err
			}

			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), sections)
			}

			return renderRoutesListSections(cmd.OutOrStdout(), sections)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func newRoutesStatusCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show detailed route status",
		RunE: func(cmd *cobra.Command, args []string) error {
			sections, err := collectRoutesStatusSections(cmd.Context(), configPath, routesStatusDeps{})
			if err != nil {
				return err
			}

			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), sections)
			}

			return renderRoutesStatusSections(cmd.OutOrStdout(), sections)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func collectRoutesListSections(ctx context.Context, cfgPath string, deps routesListDeps) ([]routeListSection, error) {
	if deps.explicitRemote == nil {
		deps.explicitRemote = resolveRoutesExplicitRemote
	}
	if deps.loadLocal == nil {
		deps.loadLocal = loadRoutesListLocalSection
	}
	if deps.listRemotes == nil {
		deps.listRemotes = remote.ListRemotes
	}
	if deps.loadRemote == nil {
		deps.loadRemote = loadRoutesListRemoteSection
	}

	if resolved, ok := deps.explicitRemote(); ok {
		section := loadRoutesListExplicitRemoteSection(ctx, deps, resolved)
		return []routeListSection{section}, nil
	}

	return collectRoutesListAggregateSections(ctx, cfgPath, deps)
}

func loadRoutesListExplicitRemoteSection(ctx context.Context, deps routesListDeps, resolved *remote.ResolvedRemote) routeListSection {
	section, _ := deps.loadRemote(ctx, resolved.DisplayName(), remote.RemoteEntry{URL: resolved.URL, Token: resolved.Token, InsecureTLS: resolved.InsecureTLS})
	return normalizeRouteListRemoteSection(section, resolved.DisplayName(), remote.RemoteEntry{URL: resolved.URL, Token: resolved.Token, InsecureTLS: resolved.InsecureTLS})
}

func collectRoutesListAggregateSections(ctx context.Context, cfgPath string, deps routesListDeps) ([]routeListSection, error) {

	type localResult struct {
		section routeListSection
	}
	type remotesResult struct {
		entries map[string]remote.RemoteEntry
		err     error
	}

	localCh := make(chan localResult, 1)
	remotesCh := make(chan remotesResult, 1)

	go func() {
		section, _ := deps.loadLocal(ctx, cfgPath)
		localCh <- localResult{section: section}
	}()
	go func() {
		entries, _, err := deps.listRemotes()
		remotesCh <- remotesResult{entries: entries, err: err}
	}()

	local := (<-localCh).section
	remotes := <-remotesCh

	sections := make([]routeListSection, 0, 1)
	sections = append(sections, normalizeRouteListLocalSection(local))

	if remotes.err != nil {
		sections = append(sections, routeListSection{Kind: "remote", Name: "remotes", Error: remotes.err.Error()})
		return sections, nil
	}

	names := sortedRemoteNames(remotes.entries)
	remoteSections := make([]routeListSection, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		i, name := i, name
		entry := remotes.entries[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			section, _ := deps.loadRemote(ctx, name, entry)
			remoteSections[i] = normalizeRouteListRemoteSection(section, name, entry)
		}()
	}
	wg.Wait()

	sections = append(sections, remoteSections...)
	return sections, nil
}

func normalizeRouteListLocalSection(section routeListSection) routeListSection {
	if section.Kind == "" {
		section.Kind = "local"
	}
	if section.Name == "" {
		section.Name = "local"
	}
	return section
}

func normalizeRouteListRemoteSection(section routeListSection, name string, entry remote.RemoteEntry) routeListSection {
	if section.Kind == "" {
		section.Kind = "remote"
	}
	if section.Name == "" {
		section.Name = name
	}
	if section.URL == "" {
		section.URL = entry.URL
	}
	return section
}

func collectRoutesStatusSections(ctx context.Context, cfgPath string, deps routesStatusDeps) ([]routeStatusSection, error) {
	if deps.explicitRemote == nil {
		deps.explicitRemote = resolveRoutesExplicitRemote
	}
	if deps.loadLocal == nil {
		deps.loadLocal = loadRoutesStatusLocalSection
	}
	if deps.listRemotes == nil {
		deps.listRemotes = remote.ListRemotes
	}
	if deps.loadRemote == nil {
		deps.loadRemote = loadRoutesStatusRemoteSection
	}

	if resolved, ok := deps.explicitRemote(); ok {
		section := loadRoutesStatusExplicitRemoteSection(ctx, deps, resolved)
		return []routeStatusSection{section}, nil
	}

	return collectRoutesStatusAggregateSections(ctx, cfgPath, deps)
}

func loadRoutesStatusExplicitRemoteSection(ctx context.Context, deps routesStatusDeps, resolved *remote.ResolvedRemote) routeStatusSection {
	section, _ := deps.loadRemote(ctx, resolved.DisplayName(), remote.RemoteEntry{URL: resolved.URL, Token: resolved.Token, InsecureTLS: resolved.InsecureTLS})
	return normalizeRouteStatusRemoteSection(section, resolved.DisplayName(), remote.RemoteEntry{URL: resolved.URL, Token: resolved.Token, InsecureTLS: resolved.InsecureTLS})
}

func collectRoutesStatusAggregateSections(ctx context.Context, cfgPath string, deps routesStatusDeps) ([]routeStatusSection, error) {

	type localResult struct {
		section routeStatusSection
	}
	type remotesResult struct {
		entries map[string]remote.RemoteEntry
		err     error
	}

	localCh := make(chan localResult, 1)
	remotesCh := make(chan remotesResult, 1)

	go func() {
		section, _ := deps.loadLocal(ctx, cfgPath)
		localCh <- localResult{section: section}
	}()
	go func() {
		entries, _, err := deps.listRemotes()
		remotesCh <- remotesResult{entries: entries, err: err}
	}()

	local := (<-localCh).section
	remotes := <-remotesCh

	sections := make([]routeStatusSection, 0, 1)
	sections = append(sections, normalizeRouteStatusLocalSection(local))

	if remotes.err != nil {
		sections = append(sections, routeStatusSection{Kind: "remote", Name: "remotes", Error: remotes.err.Error()})
		return sections, nil
	}

	names := sortedRemoteNames(remotes.entries)
	remoteSections := make([]routeStatusSection, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		i, name := i, name
		entry := remotes.entries[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			section, _ := deps.loadRemote(ctx, name, entry)
			remoteSections[i] = normalizeRouteStatusRemoteSection(section, name, entry)
		}()
	}
	wg.Wait()

	sections = append(sections, remoteSections...)
	return sections, nil
}

func normalizeRouteStatusLocalSection(section routeStatusSection) routeStatusSection {
	if section.Kind == "" {
		section.Kind = "local"
	}
	if section.Name == "" {
		section.Name = "local"
	}
	return section
}

func normalizeRouteStatusRemoteSection(section routeStatusSection, name string, entry remote.RemoteEntry) routeStatusSection {
	if section.Kind == "" {
		section.Kind = "remote"
	}
	if section.Name == "" {
		section.Name = name
	}
	if section.URL == "" {
		section.URL = entry.URL
	}
	return section
}

func renderRoutesListSections(out io.Writer, sections []routeListSection) error {
	if err := cliWriteLine(out, cliRenderTitle("Routes")); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}

	for i, section := range sections {
		if i > 0 {
			if err := cliWriteLine(out, ""); err != nil {
				return err
			}
		}
		if err := renderRoutesListSection(out, section); err != nil {
			return err
		}
	}

	return nil
}

func renderRoutesListSection(out io.Writer, section routeListSection) error {
	if err := cliWriteLine(out, routeSectionHeading(section.Kind, section.Name)); err != nil {
		return err
	}

	if len(section.Routes) > 0 {
		const imageColWidth = 45
		rows := make([][]string, 0, len(section.Routes))
		for _, route := range section.Routes {
			rows = append(rows, []string{route.Domain, truncateImage(route.Image, imageColWidth)})
		}

		table := components.NewTable(
			components.WithColumns([]components.TableColumn{
				{Title: "Domain", Width: 30},
				{Title: "Image", Width: imageColWidth},
			}),
			components.WithRows(rows),
		)

		if err := cliWriteLine(out, table.View()); err != nil {
			return err
		}
	}

	if section.Error != "" {
		return cliWriteLine(out, cliRenderWarning(section.Error))
	}

	if len(section.Routes) == 0 {
		return cliWriteLine(out, cliRenderMuted("No routes configured"))
	}

	return nil
}

func renderRoutesStatusSections(out io.Writer, sections []routeStatusSection) error {
	if err := cliWriteLine(out, cliRenderTitle("Route Status")); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}

	for i, section := range sections {
		if i > 0 {
			if err := cliWriteLine(out, ""); err != nil {
				return err
			}
		}
		if err := renderRoutesStatusSection(out, section); err != nil {
			return err
		}
	}

	return nil
}

func renderRoutesStatusSection(out io.Writer, section routeStatusSection) error {
	if err := cliWriteLine(out, routeSectionHeading(section.Kind, section.Name)); err != nil {
		return err
	}

	if len(section.Routes) > 0 {
		tree := buildRouteStatusTree(section.Routes)
		if err := cliWriteLine(out, tree.Render()); err != nil {
			return err
		}
	}

	if section.Error != "" {
		return cliWriteLine(out, cliRenderWarning(section.Error))
	}

	if len(section.Routes) == 0 {
		return cliWriteLine(out, cliRenderMuted("No routes configured"))
	}

	return nil
}

func routeSectionHeading(kind, name string) string {
	switch kind {
	case "local":
		return "Local"
	case "remote":
		if name != "" {
			return "Remote: " + name
		}
		return "Remote"
	default:
		if name != "" {
			return capitalizeKind(kind) + ": " + name
		}
		return capitalizeKind(kind)
	}
}

func capitalizeKind(kind string) string {
	if kind == "" {
		return ""
	}
	if len(kind) == 1 {
		return strings.ToUpper(kind)
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func resolveRoutesExplicitRemote() (*remote.ResolvedRemote, bool) {
	target, ok := resolveRoutesExplicitTarget()
	if !ok {
		return nil, false
	}

	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return &remote.ResolvedRemote{
			URL:         target,
			Token:       resolveRoutesTokenForTarget("", remote.RemoteEntry{}),
			InsecureTLS: resolveRoutesInsecureForTarget("", remote.RemoteEntry{}),
		}, true
	}

	remotes, err := remote.LoadRemotes("")
	if err != nil {
		return nil, false
	}

	if remotes != nil {
		if entry, found := remotes.Remotes[target]; found {
			return &remote.ResolvedRemote{
				Name:        target,
				URL:         entry.URL,
				Token:       resolveRoutesTokenForTarget(target, entry),
				InsecureTLS: resolveRoutesInsecureForTarget(target, entry),
			}, true
		}
	}

	return nil, false
}

func resolveRoutesExplicitTarget() (string, bool) {
	if target := strings.TrimSpace(remoteFlag); target != "" {
		return target, true
	}
	if target := strings.TrimSpace(os.Getenv("GORDON_REMOTE")); target != "" {
		return target, true
	}
	return "", false
}

func resolveRoutesTokenForTarget(name string, entry remote.RemoteEntry) string {
	if token := strings.TrimSpace(tokenFlag); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("GORDON_TOKEN")); token != "" {
		return token
	}
	if name != "" {
		return remote.ResolveTokenForRemote(name, entry)
	}
	return ""
}

func resolveRoutesInsecureForTarget(name string, entry remote.RemoteEntry) bool {
	if insecureTLSFlag {
		return true
	}
	if env := strings.TrimSpace(os.Getenv("GORDON_INSECURE")); env != "" {
		if value, err := strconv.ParseBool(env); err == nil {
			return value
		}
	}
	if name != "" {
		return entry.InsecureTLS
	}
	return false
}

func newRoutesTargetClient(name string, entry remote.RemoteEntry) *remote.Client {
	return remote.NewClient(entry.URL, remoteClientOptions(resolveRoutesTokenForTarget(name, entry), resolveRoutesInsecureForTarget(name, entry))...)
}

func loadRoutesListLocalSection(ctx context.Context, cfgPath string) (routeListSection, error) {
	local, err := GetLocalServices(cfgPath)
	if err != nil {
		return routeListSection{Kind: "local", Name: "local", Error: err.Error()}, nil
	}

	routes := local.GetConfigService().GetRoutes(ctx)
	section := routeListSection{Kind: "local", Name: "local", Routes: make([]routeListItem, 0, len(routes))}
	for _, route := range routes {
		section.Routes = append(section.Routes, routeListItem{Domain: route.Domain, Image: route.Image})
	}

	return section, nil
}

func loadRoutesListRemoteSection(ctx context.Context, name string, entry remote.RemoteEntry) (routeListSection, error) {
	section := routeListSection{Kind: "remote", Name: name, URL: entry.URL}
	client := newRoutesTargetClient(name, entry)
	routes, err := client.ListRoutesWithDetails(ctx)
	if err != nil {
		section.Error = err.Error()
		return section, nil
	}

	section.Routes = routeListItemsFromInfos(routes)
	return section, nil
}

func loadRoutesStatusLocalSection(ctx context.Context, cfgPath string) (routeStatusSection, error) {
	section := routeStatusSection{Kind: "local", Name: "local"}
	handle, err := resolveLocalControlPlane(cfgPath)
	if err != nil {
		section.Error = err.Error()
		return section, nil
	}
	defer handle.close()

	routes, err := handle.plane.ListRoutesWithDetails(ctx)
	if err != nil {
		section.Error = err.Error()
		return section, nil
	}

	health, err := handle.plane.GetHealth(ctx)
	if err != nil {
		section.Error = err.Error()
		health = nil
	}

	section.Routes = routeStatusItemsFromInfos(routes, health)
	return section, nil
}

func loadRoutesStatusRemoteSection(ctx context.Context, name string, entry remote.RemoteEntry) (routeStatusSection, error) {
	section := routeStatusSection{Kind: "remote", Name: name, URL: entry.URL}
	client := newRoutesTargetClient(name, entry)
	cp := NewRemoteControlPlane(client)

	routes, err := cp.ListRoutesWithDetails(ctx)
	if err != nil {
		section.Error = err.Error()
		return section, nil
	}

	health, err := cp.GetHealth(ctx)
	if err != nil {
		section.Error = err.Error()
		health = nil
	}

	section.Routes = routeStatusItemsFromInfos(routes, health)
	return section, nil
}

func routeStatusItemsFromInfos(routes []remote.RouteInfo, health map[string]*remote.RouteHealth) []routeStatusItem {
	items := make([]routeStatusItem, 0, len(routes))
	for _, route := range routes {
		item := routeStatusItem{
			Domain:          route.Domain,
			Image:           route.Image,
			ContainerID:     route.ContainerID,
			ContainerStatus: route.ContainerStatus,
			Network:         route.Network,
		}
		if item.ContainerStatus == "" {
			if routeHealth := health[route.Domain]; routeHealth != nil {
				item.ContainerStatus = routeHealth.ContainerStatus
				item.HTTPStatus = routeHealth.HTTPStatus
				item.HealthError = routeHealth.Error
			}
		}
		if item.ContainerStatus == "" {
			item.ContainerStatus = "unknown"
		}
		if item.HTTPStatus == 0 {
			if routeHealth := health[route.Domain]; routeHealth != nil {
				item.HTTPStatus = routeHealth.HTTPStatus
				item.HealthError = routeHealth.Error
			}
		}
		if item.HealthError == "" {
			if routeHealth := health[route.Domain]; routeHealth != nil {
				item.HealthError = routeHealth.Error
			}
		}
		item.Attachments = make([]routeStatusAttachment, 0, len(route.Attachments))
		for _, attachment := range route.Attachments {
			status := attachment.Status
			if status == "" {
				status = "unknown"
			}
			item.Attachments = append(item.Attachments, routeStatusAttachment{Name: attachment.Name, Image: attachment.Image, Status: status})
		}
		items = append(items, item)
	}
	return items
}

func routeListItemsFromInfos(routes []remote.RouteInfo) []routeListItem {
	items := make([]routeListItem, 0, len(routes))
	for _, route := range routes {
		items = append(items, routeListItem{Domain: route.Domain, Image: route.Image})
	}
	return items
}

func buildRouteStatusTree(routes []routeStatusItem) *components.Tree {
	sortedRoutes := make([]routeStatusItem, len(routes))
	copy(sortedRoutes, routes)
	sort.SliceStable(sortedRoutes, func(i, j int) bool {
		if sortedRoutes[i].Network != sortedRoutes[j].Network {
			return sortedRoutes[i].Network < sortedRoutes[j].Network
		}
		if sortedRoutes[i].Domain != sortedRoutes[j].Domain {
			return sortedRoutes[i].Domain < sortedRoutes[j].Domain
		}
		if sortedRoutes[i].Image != sortedRoutes[j].Image {
			return sortedRoutes[i].Image < sortedRoutes[j].Image
		}
		if sortedRoutes[i].ContainerID != sortedRoutes[j].ContainerID {
			return sortedRoutes[i].ContainerID < sortedRoutes[j].ContainerID
		}
		if sortedRoutes[i].ContainerStatus != sortedRoutes[j].ContainerStatus {
			return sortedRoutes[i].ContainerStatus < sortedRoutes[j].ContainerStatus
		}
		return sortedRoutes[i].HTTPStatus < sortedRoutes[j].HTTPStatus
	})

	infos := make([]remote.RouteInfo, 0, len(routes))
	itemsByDomain := make(map[string]routeStatusItem, len(routes))
	for _, route := range sortedRoutes {
		infos = append(infos, remote.RouteInfo{
			Domain:          route.Domain,
			Image:           route.Image,
			ContainerID:     route.ContainerID,
			ContainerStatus: route.ContainerStatus,
			Network:         route.Network,
			Attachments:     routeStatusAttachmentsToRemote(route.Attachments),
		})
		itemsByDomain[route.Domain] = route
	}

	groups, solo := groupRoutesByNetwork(infos)
	tree := components.NewTree()

	for _, group := range groups {
		g := tree.AddGroup(group.name)
		for _, route := range group.routes {
			item := itemsByDomain[route.Domain]
			node := g.AddNode(routeStatusTitle(item), item.Image)
			addRouteStatusAttachmentChildren(node, item)
		}
	}

	for _, route := range solo {
		item := itemsByDomain[route.Domain]
		node := tree.AddNode(routeStatusTitle(item), item.Image)
		addRouteStatusAttachmentChildren(node, item)
	}

	return tree
}

func routeStatusTitle(route routeStatusItem) string {
	containerStatus := route.ContainerStatus
	if containerStatus == "" {
		containerStatus = "unknown"
	}

	httpIcon := components.StatusIcon(styles.IconHTTPStatus, httpHealthToStatus(&remote.RouteHealth{HTTPStatus: route.HTTPStatus, Error: route.HealthError}))
	containerIcon := components.StatusIcon(styles.IconContainerStatus, components.ParseStatus(containerStatus))

	return httpIcon + " " + containerIcon + " " + route.Domain
}

func addRouteStatusAttachmentChildren(node *components.Node, route routeStatusItem) {
	for _, att := range route.Attachments {
		status := att.Status
		if status == "" {
			status = "unknown"
		}
		attIcon := components.StatusIcon(styles.IconContainerStatus, components.ParseStatus(status))
		node.AddChild(attIcon+" "+att.Name, att.Image)
	}
}

func routeStatusAttachmentsToRemote(attachments []routeStatusAttachment) []remote.Attachment {
	result := make([]remote.Attachment, 0, len(attachments))
	for _, att := range attachments {
		result = append(result, remote.Attachment{Name: att.Name, Image: att.Image, Status: att.Status})
	}
	return result
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
