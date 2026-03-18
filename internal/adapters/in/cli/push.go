package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/validation"
)

var newImageOpsFn = newImageOpsFromFlags

// buildConfig holds image build settings for push commands.
type buildConfig struct {
	Enabled    bool
	Platform   string
	Dockerfile string
	BuildArgs  []string
}

// imagePush holds resolved image identity for push operations.
type imagePush struct {
	Registry   string
	ImageName  string
	Version    string
	VersionRef string
	LatestRef  string
}

// pushRequest holds all inputs for the push command.
type pushRequest struct {
	ImageArg  string
	Domain    string
	Tag       string
	Build     buildConfig
	NoDeploy  bool
	NoConfirm bool
}

func newPushCmd() *cobra.Command {
	var (
		noDeploy   bool
		noConfirm  bool
		build      bool
		platform   string
		tag        string
		dockerfile string
		buildArgs  []string
		domainFlag string
	)

	cmd := &cobra.Command{
		Use:   "push [image]",
		Short: "Tag, push, and optionally deploy an image",
		Long: `Tags a local image for the Gordon registry and pushes it.
Uses git tags for versioning. Optionally triggers deployment after push.

The image argument is optional. Resolution order:
  1. If --domain is provided, uses the legacy domain-based lookup
  2. If an image name is provided as argument, resolves domain(s) from the backend
  3. If no argument, auto-detects from Dockerfile labels or current directory name

With --build, builds the image first using docker buildx.

Examples:
  gordon push --build --remote ...
  gordon push myapp --build --remote ...
  gordon push --domain myapp.example.com --build --remote ...
  gordon push myapp --tag v1.2.0 --no-deploy --remote ...
  gordon push --build --build-arg CGO_ENABLED=0 --remote ...`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var imageArg string
			if len(args) > 0 {
				imageArg = args[0]
			}
			return runPush(cmd.Context(), pushRequest{
				ImageArg:  imageArg,
				Domain:    domainFlag,
				Tag:       tag,
				Build:     buildConfig{Enabled: build, Platform: platform, Dockerfile: dockerfile, BuildArgs: buildArgs},
				NoDeploy:  noDeploy,
				NoConfirm: noConfirm,
			})
		},
	}

	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "Push only, don't trigger deploy")
	cmd.Flags().BoolVar(&noConfirm, "no-confirm", false, "Skip deploy confirmation prompt")
	cmd.Flags().BoolVar(&build, "build", false, "Build the image first using docker buildx")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform (used with --build)")
	cmd.Flags().StringVarP(&dockerfile, "file", "f", "", "Path to Dockerfile (default: ./Dockerfile, used with --build)")
	cmd.Flags().StringVar(&tag, "tag", "", "Override version tag (default: CI tag ref or git describe)")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Additional build args (used with --build)")
	cmd.Flags().StringVar(&domainFlag, "domain", "", "Explicit domain override (legacy mode)")

	return cmd
}

func resolveDockerfile(dockerfile string, build bool) (string, error) {
	if dockerfile != "" && !build {
		return "", fmt.Errorf("--file can only be used with --build")
	}
	if dockerfile == "" {
		return "Dockerfile", nil
	}
	return dockerfile, nil
}

func resolveImageRefs(registry, imageName, version string) (versionRef, latestRef string) {
	versionRef = fmt.Sprintf("%s/%s:%s", registry, imageName, version)
	latestRef = fmt.Sprintf("%s/%s:latest", registry, imageName)
	return versionRef, latestRef
}

// resolveRoute determines the registry, image name, and domain from the input mode.
func resolveRoute(ctx context.Context, cp ControlPlane, imageArg, domainFlag, dockerfile string) (registry, imageName, pushDomain string, err error) {
	if domainFlag != "" {
		return resolveFromDomain(ctx, cp, domainFlag)
	}

	if imageArg == "" {
		imageArg, err = detectImageName(dockerfile)
		if err != nil {
			return "", "", "", err
		}
		fmt.Printf("Detected image: %s\n", styles.Theme.Bold.Render(imageArg))
	}

	return resolveFromImage(ctx, cp, imageArg, dockerfile)
}

// resolveVersion determines and validates the version tag.
func resolveVersion(ctx context.Context, tag string) (string, error) {
	version := determineVersion(ctx, tag)
	if version != "latest" {
		if err := validation.ValidateReference(version); err != nil {
			return "", fmt.Errorf("invalid version tag %q: %w", version, err)
		}
	}
	return version, nil
}

func runPush(ctx context.Context, req pushRequest) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()

	dockerfile, err := resolveDockerfile(req.Build.Dockerfile, req.Build.Enabled)
	if err != nil {
		return err
	}

	registry, imageName, pushDomain, err := resolveRoute(ctx, handle.plane, req.ImageArg, req.Domain, dockerfile)
	if err != nil {
		return err
	}

	version, err := resolveVersion(ctx, req.Tag)
	if err != nil {
		return err
	}

	for _, ba := range req.Build.BuildArgs {
		if err := validateBuildArg(ba); err != nil {
			return err
		}
	}

	img := imagePush{
		Registry:  registry,
		ImageName: imageName,
		Version:   version,
	}
	img.VersionRef, img.LatestRef = resolveImageRefs(registry, imageName, version)

	imageOps, err := newImageOpsFn()
	if err != nil {
		return err
	}

	fmt.Printf("Image:  %s\n", styles.Theme.Bold.Render(img.VersionRef))
	if version != "latest" {
		fmt.Printf("Also:   %s\n", styles.Theme.Bold.Render(img.LatestRef))
	}
	fmt.Printf("Domain: %s\n", styles.Theme.Bold.Render(pushDomain))

	skipExplicitDeploy := shouldSkipDeploy(ctx, handle.plane, imageName, req.NoDeploy)

	build := buildConfig{
		Enabled:    req.Build.Enabled,
		Platform:   req.Build.Platform,
		Dockerfile: dockerfile,
		BuildArgs:  req.Build.BuildArgs,
	}

	if build.Enabled {
		if err := buildAndPush(ctx, imageOps, build, img); err != nil {
			return err
		}
	} else {
		if err := tagAndPush(ctx, imageOps, img); err != nil {
			return err
		}
	}

	fmt.Println(styles.RenderSuccess("Push complete"))

	if !skipExplicitDeploy {
		return deployAfterPush(ctx, handle.plane, pushDomain, req.NoConfirm)
	}

	return nil
}

// shouldSkipDeploy signals deploy intent and returns whether the explicit deploy
// should be skipped (either because --no-deploy was set, or the token lacks scope).
func shouldSkipDeploy(ctx context.Context, plane ControlPlane, imageName string, noDeploy bool) bool {
	// Always call DeployIntent to suppress server-side auto-deploy, even with --no-deploy.
	// Without this, the server's event listener would still auto-deploy the pushed image.
	if err := plane.DeployIntent(ctx, imageName); err != nil {
		if isInsufficientScope(err) {
			fmt.Fprintln(os.Stderr, "info: deploy intent skipped (insufficient scope), server will auto-deploy on image receive")
			return true
		}
		// Non-fatal: worst case we get a redundant deploy via event
		fmt.Fprintf(os.Stderr, "warning: failed to register deploy intent: %v\n", err)
	}
	return noDeploy
}

// isInsufficientScope returns true if the error is an HTTP 403 Forbidden response,
// meaning the token lacks the required admin scope.
func isInsufficientScope(err error) bool {
	var httpErr *remote.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusForbidden
}

// resolveFromDomain resolves image info from a domain name (legacy mode).
func resolveFromDomain(ctx context.Context, cp ControlPlane, pushDomain string) (registry, imageName, resolvedDomain string, err error) {
	route, err := cp.GetRoute(ctx, pushDomain)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get route for domain %q: %w", pushDomain, err)
	}

	registry, imageName, _ = parseImageRef(route.Image)
	if registry == "" || imageName == "" {
		return "", "", "", fmt.Errorf("cannot parse registry/image from route image: %s", route.Image)
	}

	return registry, imageName, pushDomain, nil
}

// resolveFromImage resolves domain(s) from an image name using the backend.
func resolveFromImage(ctx context.Context, cp ControlPlane, imageArg, dockerfile string) (registry, imageName, resolvedDomain string, err error) {
	// First, query the backend to find routes for this image
	routes, err := cp.FindRoutesByImage(ctx, imageArg)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to find routes for image %q: %w", imageArg, err)
	}

	// Filter out preview routes (domains containing "--") so they don't
	// pollute the selection when pushing the base image.
	filtered := routes[:0]
	for _, r := range routes {
		if !strings.Contains(r.Domain, domain.DefaultPreviewSeparator) {
			filtered = append(filtered, r)
		}
	}
	routes = filtered

	if len(routes) == 0 {
		// bootstrap command is registered in bootstrap.go (see issue #98)
		return "", "", "", fmt.Errorf(
			"no route configured for image %q\n\nFor a first deploy, use 'gordon bootstrap <domain> %s'\nOr configure the route directly with 'gordon routes add <domain> %s'\nIf this is an attachment image, use 'gordon attachments push %s'",
			imageArg,
			imageArg,
			imageArg,
			imageArg,
		)
	}

	// Pick the target domain
	var targetRoute domain.Route
	if len(routes) == 1 {
		targetRoute = routes[0]
	} else {
		// Multiple domains: check Dockerfile labels first, then prompt
		targetRoute, err = selectDomain(routes, dockerfile)
		if err != nil {
			return "", "", "", err
		}
	}

	registry, imageName, _ = parseImageRef(targetRoute.Image)
	if registry == "" || imageName == "" {
		return "", "", "", fmt.Errorf("cannot parse registry/image from route image: %s", targetRoute.Image)
	}

	return registry, imageName, targetRoute.Domain, nil
}

// selectDomain picks the target domain from multiple routes.
// Checks Dockerfile labels first, then falls back to interactive selection.
func selectDomain(routes []domain.Route, dockerfile string) (domain.Route, error) {
	// Try to resolve from Dockerfile labels
	labels := parseDockerfileLabels(dockerfile)
	labelDomain := labels[domain.LabelDomain]
	labelDomains := labels[domain.LabelDomains]

	// Collect domains from labels
	var labelDomainList []string
	if labelDomain != "" {
		labelDomainList = append(labelDomainList, labelDomain)
	}
	if labelDomains != "" {
		for _, d := range strings.Split(labelDomains, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				labelDomainList = append(labelDomainList, d)
			}
		}
	}

	// Try to find a matching route from labels
	if len(labelDomainList) > 0 {
		routeMap := make(map[string]domain.Route, len(routes))
		for _, r := range routes {
			routeMap[r.Domain] = r
		}
		for _, ld := range labelDomainList {
			if r, ok := routeMap[ld]; ok {
				return r, nil
			}
		}
	}

	// Fall back to interactive selection
	items := make([]string, 0, len(routes))
	for _, r := range routes {
		items = append(items, fmt.Sprintf("%s  %s", r.Domain, styles.Theme.Muted.Render(r.Image)))
	}

	selected, err := components.RunSelector(
		"Multiple domains found for this image. Select target:",
		items,
		"",
	)
	if err != nil {
		return domain.Route{}, fmt.Errorf("selection error: %w", err)
	}
	if selected == "" {
		return domain.Route{}, fmt.Errorf("no domain selected")
	}

	// Extract domain from the selected display string
	for i, item := range items {
		if item == selected {
			return routes[i], nil
		}
	}

	return domain.Route{}, fmt.Errorf("selected domain not found")
}

// detectImageName auto-detects the image name from context.
// Resolution order:
// 1. Dockerfile label gordon.domain (if Dockerfile exists)
// 2. Current directory name
func detectImageName(dockerfile string) (string, error) {
	// Try Dockerfile labels
	labels := parseDockerfileLabels(dockerfile)
	if d := labels[domain.LabelDomain]; d != "" {
		// Strip the registry prefix if it looks like a full image ref
		_, name, _ := parseImageRef(d)
		if name != "" {
			return name, nil
		}
		return d, nil
	}

	// Fall back to current directory name
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot detect image name: %w", err)
	}

	dirName := filepath.Base(cwd)
	if dirName == "." || dirName == "/" {
		return "", fmt.Errorf("cannot detect image name from current directory; provide an image name or use --domain")
	}

	return dirName, nil
}

// parseDockerfileLabels extracts LABEL instructions from a Dockerfile.
// Returns a map of label key -> value. Only parses gordon.* labels.
func parseDockerfileLabels(dockerfile string) map[string]string {
	labels := make(map[string]string)

	f, err := os.Open(dockerfile)
	if err != nil {
		return labels
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Match LABEL instructions
		if !strings.HasPrefix(strings.ToUpper(line), "LABEL ") {
			continue
		}

		// Parse "LABEL key=value" or "LABEL key=\"value\""
		labelContent := strings.TrimSpace(line[6:])
		for _, pair := range splitLabelPairs(labelContent) {
			key, value, ok := parseLabelPair(pair)
			if ok && strings.HasPrefix(key, "gordon.") {
				labels[key] = value
			}
		}
	}

	return labels
}

// splitLabelPairs splits LABEL content into key=value pairs,
// handling quoted values that may contain spaces.
func splitLabelPairs(content string) []string {
	var pairs []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(content); i++ {
		ch := content[i]
		if inQuote {
			current.WriteByte(ch)
			if ch == quoteChar {
				inQuote = false
			}
		} else if ch == '"' || ch == '\'' {
			inQuote = true
			quoteChar = ch
			current.WriteByte(ch)
		} else if ch == ' ' || ch == '\t' {
			if current.Len() > 0 {
				pairs = append(pairs, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		pairs = append(pairs, current.String())
	}

	return pairs
}

// parseLabelPair parses a single "key=value" or "key=\"value\"" pair.
func parseLabelPair(pair string) (key, value string, ok bool) {
	idx := strings.Index(pair, "=")
	if idx == -1 {
		return "", "", false
	}
	key = pair[:idx]
	value = pair[idx+1:]
	// Strip surrounding quotes
	value = strings.Trim(value, "\"'")
	return key, value, true
}

func determineVersion(ctx context.Context, tag string) string {
	version := tag
	if version == "" {
		version = versionFromTagRefs(os.Getenv)
	}
	if version == "" {
		version = getGitVersion(ctx)
	}
	if version == "" {
		version = "latest"
	}
	return version
}

func versionFromTagRefs(getenv func(string) string) string {
	if ref := strings.TrimSpace(getenv("GITHUB_REF")); ref != "" {
		if tag := parseTagRef(ref); tag != "" {
			return tag
		}
	}

	if strings.TrimSpace(getenv("GITHUB_REF_TYPE")) == "tag" {
		if tag := strings.TrimSpace(getenv("GITHUB_REF_NAME")); tag != "" {
			return tag
		}
	}

	if tag := strings.TrimSpace(getenv("CI_COMMIT_TAG")); tag != "" {
		return tag
	}

	if ref := strings.TrimSpace(getenv("BUILD_SOURCEBRANCH")); ref != "" {
		if tag := parseTagRef(ref); tag != "" {
			return tag
		}
	}

	return ""
}

func parseTagRef(ref string) string {
	ref = strings.TrimSpace(strings.TrimSuffix(ref, "^{}"))
	if !strings.HasPrefix(ref, "refs/tags/") {
		return ""
	}
	tag := strings.TrimPrefix(ref, "refs/tags/")
	if tag == "" {
		return ""
	}
	return tag
}

func buildAndPush(ctx context.Context, ops pushImageOps, build buildConfig, img imagePush) error {
	if _, err := os.Stat(build.Dockerfile); os.IsNotExist(err) {
		return fmt.Errorf("dockerfile not found: %s", build.Dockerfile)
	}

	// Build and load into local daemon (NOT --push).
	// The native registry push client handles chunked uploads to stay
	// within Cloudflare's 100MB per-request limit.
	fmt.Println("\nBuilding image...")
	if err := ops.Build(ctx, buildImageArgs(ctx, img.Version, build.Platform, build.Dockerfile, build.BuildArgs, img.VersionRef, img.LatestRef)); err != nil {
		return err
	}

	fmt.Println("Pushing...")
	if err := ops.Push(ctx, img.LatestRef); err != nil {
		return fmt.Errorf("failed to push %s: %w", img.LatestRef, err)
	}
	if img.Version != "latest" {
		if err := ops.Push(ctx, img.VersionRef); err != nil {
			return fmt.Errorf("failed to push %s: %w", img.VersionRef, err)
		}
	}

	return nil
}

// standardBuildArgs returns the standard set of git-related build args as
// explicit KEY=VALUE pairs. User-supplied args are appended after and take
// precedence (Docker uses the last occurrence of a duplicate key).
func standardBuildArgs(ctx context.Context, version string) []string {
	gitSHA := resolveGitSHA(ctx)
	buildTime := time.Now().UTC().Format(time.RFC3339)
	return []string{
		"VERSION=" + version,
		"GIT_TAG=" + version,
		"GIT_SHA=" + gitSHA,
		"BUILD_TIME=" + buildTime,
	}
}

// resolveGitSHA returns the short git SHA of HEAD, or "unknown" if unavailable.
func resolveGitSHA(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output() // #nosec G204
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// buildImageArgs constructs the docker buildx build arguments.
// Uses --load instead of --push so the image is loaded into the local
// daemon, allowing docker push to handle the upload with chunked requests.
func buildImageArgs(ctx context.Context, version, platform, dockerfile string, buildArgs []string, versionRef, latestRef string) []string {
	args := []string{
		"buildx", "build",
		"--platform", platform,
		"-f", dockerfile,
		"-t", latestRef,
	}
	if version != "latest" {
		args = append(args, "-t", versionRef)
	}

	// Inject standard git build args as explicit KEY=VALUE pairs.
	// User-supplied --build-arg flags are appended AFTER so they override defaults.
	for _, ba := range standardBuildArgs(ctx, version) {
		args = append(args, "--build-arg", ba)
	}
	for _, ba := range buildArgs {
		args = append(args, "--build-arg", ba)
	}

	args = append(args, "--load", ".")
	return args
}

func tagAndPush(ctx context.Context, ops pushImageOps, img imagePush) error {
	localImage := fmt.Sprintf("%s/%s", img.Registry, img.ImageName)

	fmt.Println("\nChecking local image...")
	exists, err := ops.Exists(ctx, localImage)
	if err != nil {
		return fmt.Errorf("failed to inspect local image %s: %w", localImage, err)
	}
	if !exists {
		return fmt.Errorf("local image %s not found; build and tag it before pushing", localImage)
	}

	fmt.Println("Tagging...")
	if err := ops.Tag(ctx, localImage, img.VersionRef); err != nil {
		return fmt.Errorf("failed to tag %s: %w", img.VersionRef, err)
	}
	if img.Version != "latest" {
		if err := ops.Tag(ctx, localImage, img.LatestRef); err != nil {
			return fmt.Errorf("failed to tag %s: %w", img.LatestRef, err)
		}
	}

	fmt.Println("Pushing...")
	if err := ops.Push(ctx, img.VersionRef); err != nil {
		return fmt.Errorf("failed to push %s: %w", img.VersionRef, err)
	}
	if img.Version != "latest" {
		if err := ops.Push(ctx, img.LatestRef); err != nil {
			return fmt.Errorf("failed to push %s: %w", img.LatestRef, err)
		}
	}
	return nil
}

func deployAfterPush(ctx context.Context, cp ControlPlane, pushDomain string, noConfirm bool) error {
	if !noConfirm {
		confirmed, err := components.RunConfirm("Deploy now?", components.WithDefaultYes())
		if err != nil {
			return err
		}
		if !confirmed {
			return nil
		}
	}

	var (
		result *remote.DeployResult
		err    error
	)
	if remoteCP, ok := cp.(*remoteControlPlane); ok {
		result, err = deployWithSpinner(ctx, remoteCP.client, pushDomain)
	} else {
		result, err = cp.Deploy(ctx, pushDomain)
	}
	if err != nil {
		return fmt.Errorf("failed to deploy: %w", err)
	}
	containerID := result.ContainerID
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}
	fmt.Println(styles.RenderSuccess(fmt.Sprintf("Deployed %s (container: %s)", pushDomain, containerID)))
	return nil
}

func deployWithSpinner(ctx context.Context, client *remote.Client, pushDomain string) (*remote.DeployResult, error) {
	if !isInteractiveTerminal() {
		fmt.Printf("Deploying %s...\n", pushDomain)
		return client.Deploy(ctx, pushDomain)
	}

	done := make(chan deployOutcome, 1)
	go func() {
		result, err := client.Deploy(ctx, pushDomain)
		done <- deployOutcome{result: result, err: err}
	}()

	model := newDeploySpinnerModel(pushDomain, done)
	final, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	fmt.Print("\r\033[K")
	if err != nil {
		return nil, err
	}

	deployModel, ok := final.(deploySpinnerModel)
	if !ok {
		return nil, fmt.Errorf("spinner exited with unexpected model type %T", final)
	}
	if !deployModel.finished {
		return nil, fmt.Errorf("deploy spinner exited before deploy result was received")
	}

	return deployModel.outcome.result, deployModel.outcome.err
}

type deployOutcome struct {
	result *remote.DeployResult
	err    error
}

type deployDoneMsg deployOutcome

type deploySpinnerModel struct {
	spinner  components.SpinnerModel
	done     <-chan deployOutcome
	outcome  deployOutcome
	finished bool
}

func newDeploySpinnerModel(pushDomain string, done <-chan deployOutcome) deploySpinnerModel {
	return deploySpinnerModel{
		spinner: components.NewSpinner(
			components.WithMessage(fmt.Sprintf("Deploying %s...", pushDomain)),
			components.WithSpinnerType(components.SpinnerMiniDot),
		),
		done: done,
	}
}

func (m deploySpinnerModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Init(), waitForDeployDone(m.done))
}

func (m deploySpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case deployDoneMsg:
		m.outcome = deployOutcome(msg)
		m.finished = true
		return m, tea.Quit
	default:
		updated, cmd := m.spinner.Update(msg)
		spinnerModel, ok := updated.(components.SpinnerModel)
		if ok {
			m.spinner = spinnerModel
		}
		return m, cmd
	}
}

func (m deploySpinnerModel) View() string {
	return m.spinner.View()
}

func waitForDeployDone(done <-chan deployOutcome) tea.Cmd {
	return func() tea.Msg {
		return deployDoneMsg(<-done)
	}
}

func isInteractiveTerminal() bool {
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// parseImageRef splits "registry/name:tag" into components.
func parseImageRef(image string) (registry, name, tag string) {
	// Handle "reg.bnema.dev/jp-bot:latest" or "reg.bnema.dev/jp-bot"
	parts := strings.SplitN(image, "/", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	registry = parts[0]
	nameTag := parts[1]
	if idx := strings.LastIndex(nameTag, ":"); idx != -1 {
		name = nameTag[:idx]
		tag = nameTag[idx+1:]
	} else {
		name = nameTag
		tag = "latest"
	}
	return
}

// getGitVersion returns git describe output, or empty string if unavailable.
// When it falls back it prints a warning to stderr so the user knows the
// image will be tagged "latest" rather than a real version.
func getGitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--dirty").Output() // #nosec G204
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: unable to determine git tag (%v) — image version will be 'latest'. Tag your repo to get versioned images.\n", err)
		return ""
	}
	return strings.TrimSpace(string(out))
}

// buildArgPattern matches KEY=VALUE where KEY starts with a letter or underscore.
var buildArgPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*=.*$`)

func validateBuildArg(arg string) error {
	if !buildArgPattern.MatchString(arg) {
		return fmt.Errorf("invalid build arg %q: must match KEY=VALUE where KEY is [a-zA-Z_][a-zA-Z0-9_]*", arg)
	}
	return nil
}
