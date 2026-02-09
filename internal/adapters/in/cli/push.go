package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/pkg/validation"
)

func newPushCmd() *cobra.Command {
	var (
		noDeploy   bool
		noConfirm  bool
		build      bool
		platform   string
		tag        string
		dockerfile string
		buildArgs  []string
	)

	cmd := &cobra.Command{
		Use:   "push <domain>",
		Short: "Tag, push, and optionally deploy an image",
		Long: `Tags a local image for the Gordon registry and pushes it.
Uses git tags for versioning. Optionally triggers deployment after push.

With --build, builds the image first using docker buildx.

Examples:
  gordon push myapp.example.com --remote ...
  gordon push myapp.example.com --build --remote ...
  gordon push myapp.example.com --tag v1.2.0 --no-deploy --remote ...
  gordon push myapp.example.com --build --build-arg CGO_ENABLED=0 --remote ...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cmd.Context(), args[0], tag, build, platform, dockerfile, buildArgs, noDeploy, noConfirm)
		},
	}

	cmd.Flags().BoolVar(&noDeploy, "no-deploy", false, "Push only, don't trigger deploy")
	cmd.Flags().BoolVar(&noConfirm, "no-confirm", false, "Skip deploy confirmation prompt")
	cmd.Flags().BoolVar(&build, "build", false, "Build the image first using docker buildx")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform (used with --build)")
	cmd.Flags().StringVarP(&dockerfile, "file", "f", "", "Path to Dockerfile (default: ./Dockerfile, used with --build)")
	cmd.Flags().StringVar(&tag, "tag", "", "Override version tag (default: git describe)")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Additional build args (used with --build)")

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

func runPush(ctx context.Context, pushDomain, tag string, build bool, platform string, dockerfile string, buildArgs []string, noDeploy bool, noConfirm bool) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()

	dockerfile, err = resolveDockerfile(dockerfile, build)
	if err != nil {
		return err
	}

	route, err := handle.plane.GetRoute(ctx, pushDomain)
	if err != nil {
		return fmt.Errorf("failed to get route: %w", err)
	}

	registry, imageName, _ := parseImageRef(route.Image)
	if registry == "" || imageName == "" {
		return fmt.Errorf("cannot parse registry/image from route image: %s", route.Image)
	}

	version := determineVersion(ctx, tag)
	if version != "latest" {
		if err := validation.ValidateReference(version); err != nil {
			return fmt.Errorf("invalid version tag %q: %w", version, err)
		}
	}

	for _, ba := range buildArgs {
		if err := validateBuildArg(ba); err != nil {
			return err
		}
	}

	versionRef, latestRef := resolveImageRefs(registry, imageName, version)

	fmt.Printf("Image: %s\n", styles.Theme.Bold.Render(versionRef))
	if version != "latest" {
		fmt.Printf("Also:  %s\n", styles.Theme.Bold.Render(latestRef))
	}

	if build {
		if err := buildAndPush(ctx, version, platform, dockerfile, buildArgs, versionRef, latestRef); err != nil {
			return err
		}
	} else {
		if err := tagAndPush(ctx, registry, imageName, version, versionRef, latestRef); err != nil {
			return err
		}
	}

	fmt.Println(styles.RenderSuccess("Push complete"))

	if !noDeploy {
		return deployAfterPush(ctx, handle.plane, pushDomain, noConfirm)
	}

	return nil
}

func determineVersion(ctx context.Context, tag string) string {
	version := tag
	if version == "" {
		version = getGitVersion(ctx)
	}
	if version == "" {
		version = "latest"
	}
	return version
}

func buildAndPush(ctx context.Context, version, platform, dockerfile string, buildArgs []string, versionRef, latestRef string) error {
	if _, err := os.Stat(dockerfile); os.IsNotExist(err) {
		return fmt.Errorf("dockerfile not found: %s", dockerfile)
	}

	// Build and load into local daemon (NOT --push).
	// BuildKit's --push uses monolithic blob uploads that exceed
	// Cloudflare's 100MB per-request limit. Loading locally then
	// using docker push gives us chunked uploads (~5MB per request).
	fmt.Println("\nBuilding image...")
	buildCmd := exec.CommandContext(ctx, "docker", buildImageArgs(version, platform, dockerfile, buildArgs, versionRef, latestRef)...) // #nosec G204
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("docker buildx build failed: %w", err)
	}

	fmt.Println("Pushing...")
	if err := dockerPush(ctx, latestRef); err != nil {
		return fmt.Errorf("failed to push %s: %w", latestRef, err)
	}
	if version != "latest" {
		if err := dockerPush(ctx, versionRef); err != nil {
			return fmt.Errorf("failed to push %s: %w", versionRef, err)
		}
	}

	return nil
}

// buildImageArgs constructs the docker buildx build arguments.
// Uses --load instead of --push so the image is loaded into the local
// daemon, allowing docker push to handle the upload with chunked requests.
func buildImageArgs(version, platform, dockerfile string, buildArgs []string, versionRef, latestRef string) []string {
	args := []string{
		"buildx", "build",
		"--platform", platform,
		"-f", dockerfile,
		"-t", latestRef,
		"--build-arg", "VERSION=" + version,
	}
	if version != "latest" {
		args = append(args, "-t", versionRef)
	}
	for _, ba := range buildArgs {
		args = append(args, "--build-arg", ba)
	}
	args = append(args, "--load", ".")
	return args
}

func tagAndPush(ctx context.Context, registry, imageName, version, versionRef, latestRef string) error {
	localImage := fmt.Sprintf("%s/%s", registry, imageName)

	fmt.Println("\nChecking local image...")
	if !dockerImageExists(ctx, localImage) {
		return fmt.Errorf("local image %s not found; build and tag it before pushing", localImage)
	}

	fmt.Println("Tagging...")
	if err := dockerTag(ctx, localImage, versionRef); err != nil {
		return fmt.Errorf("failed to tag %s: %w", versionRef, err)
	}
	if version != "latest" {
		if err := dockerTag(ctx, localImage, latestRef); err != nil {
			return fmt.Errorf("failed to tag %s: %w", latestRef, err)
		}
	}

	fmt.Println("Pushing...")
	if err := dockerPush(ctx, versionRef); err != nil {
		return fmt.Errorf("failed to push %s: %w", versionRef, err)
	}
	if version != "latest" {
		if err := dockerPush(ctx, latestRef); err != nil {
			return fmt.Errorf("failed to push %s: %w", latestRef, err)
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

// getGitVersion returns the latest git tag, or empty string if unavailable.
func getGitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func dockerTag(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "docker", "tag", src, dst)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dockerPush(ctx context.Context, ref string) error {
	cmd := exec.CommandContext(ctx, "docker", "push", ref)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func dockerImageExists(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image) // #nosec G204
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// buildArgPattern matches KEY=VALUE where KEY starts with a letter or underscore.
var buildArgPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*=.*$`)

func validateBuildArg(arg string) error {
	if !buildArgPattern.MatchString(arg) {
		return fmt.Errorf("invalid build arg %q: must match KEY=VALUE where KEY is [a-zA-Z_][a-zA-Z0-9_]*", arg)
	}
	return nil
}
