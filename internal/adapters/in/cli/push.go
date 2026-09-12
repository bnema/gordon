package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
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
	SourceRef  string
	Version    string
	VersionRef string
	LatestRef  string
}

// pushRequest holds all inputs for the push command.
type pushRequest struct {
	ImageArg string
	Tag      string
	Build    buildConfig
}

func newPushCmd() *cobra.Command {
	var (
		build      bool
		platform   string
		tag        string
		dockerfile string
		buildArgs  []string
	)

	cmd := &cobra.Command{
		Use:   "push [image]",
		Short: "Tag and push an image to the Gordon registry",
		Long: `Tags a local image for the Gordon registry and pushes it.
Uses git tags for versioning. Push transfers OCI content only: it never
deploys. Deploy separately with ` + "`gordon apps deploy`" + ` after applying
the manifest that references the pushed tag.

Examples:
  gordon push myapp --build --remote ...
  gordon push myapp:v1.2.3 --tag v1.2.3 --remote ...
  gordon push registry.example.com/myapp:v1.2.3 --build --remote ...
  gordon push --build --build-arg CGO_ENABLED=0 --remote ...`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var imageArg string
			if len(args) > 0 {
				imageArg = args[0]
			}
			return runPush(cmd.Context(), cmd.OutOrStdout(), pushRequest{
				ImageArg: imageArg,
				Tag:      tag,
				Build:    buildConfig{Enabled: build, Platform: platform, Dockerfile: dockerfile, BuildArgs: buildArgs},
			})
		},
	}

	cmd.Flags().BoolVar(&build, "build", false, "Build the image first using docker buildx")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform (used with --build)")
	cmd.Flags().StringVarP(&dockerfile, "file", "f", "", "Path to Dockerfile (default: ./Dockerfile, used with --build)")
	cmd.Flags().StringVar(&tag, "tag", "", "Override pushed version tag (default: CI tag ref or git describe)")
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

type pushArgKind string

const (
	pushArgKindImage        pushArgKind = "image"
	pushArgKindLegacyDomain pushArgKind = "legacy-domain"
)

type classifiedPushArg struct {
	kind         pushArgKind
	sourceRef    string
	repository   string
	legacyDomain string
}

func classifyPushArgument(arg string) classifiedPushArg {
	if looksLikeLegacyDomain(arg) {
		return classifiedPushArg{kind: pushArgKindLegacyDomain, legacyDomain: arg}
	}

	_, repository, _ := parseImageRef(arg)
	if repository == "" {
		repository, _ = validation.ParseImageReference(arg)
	}
	return classifiedPushArg{kind: pushArgKindImage, sourceRef: arg, repository: repository}
}

func looksLikeLegacyDomain(arg string) bool {
	if arg == "" || strings.Contains(arg, "/") {
		return false
	}
	host := arg
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return strings.Contains(host, ".")
}

// resolveImageTarget determines the registry and image name from the input.
// Push transfers OCI content only and never triggers deploys or route
// lookups. The deploy target comes from the app manifest applied
// separately via `gordon apps apply`.
func resolveImageTarget(out io.Writer, imageArg, dockerfile string) (registry, imageName, sourceRef string, err error) {
	if imageArg == "" {
		imageArg, err = detectImageName(dockerfile)
		if err != nil {
			return "", "", "", err
		}
		if err := cliWritef(out, "Detected image: %s\n", styles.Theme.Bold.Render(imageArg)); err != nil {
			return "", "", "", err
		}
	}
	if looksLikeLegacyDomain(imageArg) {
		return "", "", "", fmt.Errorf("domain-style push targets are retired: push an image name, then `gordon apps apply` (got %q)", imageArg)
	}
	classified := classifyPushArgument(imageArg)
	registry, imageName, _ = parseImageRef(classified.sourceRef)
	if registry == "" || imageName == "" {
		return "", "", "", fmt.Errorf("cannot parse registry/image from %q", imageArg)
	}
	return registry, imageName, classified.sourceRef, nil
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

func runPush(ctx context.Context, out io.Writer, req pushRequest) error {
	dockerfile, inferredRemote, handle, err := resolvePushTarget(ctx, out, req)
	if err != nil {
		return err
	}
	defer handle.close()

	registry, imageName, sourceRef, err := resolveImageTarget(out, req.ImageArg, dockerfile)
	if err != nil {
		return err
	}

	version, err := resolveVersion(ctx, req.Tag)
	if err != nil {
		return err
	}
	if err := validateBuildArgsList(req.Build.BuildArgs); err != nil {
		return err
	}

	img := imagePush{Registry: registry, ImageName: imageName, SourceRef: sourceRef, Version: version}
	img.VersionRef, img.LatestRef = resolveImageRefs(registry, imageName, version)

	imageOps, err := newPushImageOps(inferredRemote)
	if err != nil {
		return err
	}

	if err := cliWritef(out, "Image:  %s\n", styles.Theme.Bold.Render(img.VersionRef)); err != nil {
		return err
	}
	if version != "latest" {
		if err := cliWritef(out, "Also:   %s\n", styles.Theme.Bold.Render(img.LatestRef)); err != nil {
			return err
		}
	}
	build := buildConfig{Enabled: req.Build.Enabled, Platform: req.Build.Platform, Dockerfile: dockerfile, BuildArgs: req.Build.BuildArgs}
	if err := pushResolvedImage(ctx, out, imageOps, build, img); err != nil {
		return err
	}

	if err := cliWriteLine(out, styles.RenderSuccess("Push complete")); err != nil {
		return err
	}
	return nil
}

func resolvePushTarget(ctx context.Context, out io.Writer, req pushRequest) (dockerfile string, inferredRemote *remote.ResolvedRemote, handle *controlPlaneHandle, err error) {
	dockerfile, err = resolveDockerfile(req.Build.Dockerfile, req.Build.Enabled)
	if err != nil {
		return "", nil, nil, err
	}
	inferredRemote, err = inferPushRemote(ctx, req.ImageArg, "", dockerfile)
	if err != nil {
		return "", nil, nil, err
	}
	if inferredRemote != nil {
		handle = newRemoteControlPlaneHandle(inferredRemote)
		if err := cliWritef(out, "Remote: %s %s\n", styles.Theme.Bold.Render(inferredRemote.DisplayName()), styles.Theme.Muted.Render("(auto-detected)")); err != nil {
			return "", nil, nil, err
		}
		return dockerfile, inferredRemote, handle, nil
	}
	handle, err = resolveControlPlane(cliConfigPath)
	if err != nil {
		return "", nil, nil, err
	}
	return dockerfile, nil, handle, nil
}

func validateBuildArgsList(buildArgs []string) error {
	for _, ba := range buildArgs {
		if err := validateBuildArg(ba); err != nil {
			return err
		}
	}
	return nil
}

func newPushImageOps(inferredRemote *remote.ResolvedRemote) (pushImageOps, error) {
	if inferredRemote != nil {
		return newImageOpsForResolvedRemote(inferredRemote)
	}
	return newImageOpsFn()
}

func pushResolvedImage(ctx context.Context, out io.Writer, imageOps pushImageOps, build buildConfig, img imagePush) error {
	if build.Enabled {
		return buildAndPush(ctx, out, imageOps, build, img)
	}
	return tagAndPush(ctx, out, imageOps, img)
}

// detectImageName resolves the image name from Dockerfile labels or the
// current directory name.
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
		return "", fmt.Errorf("cannot detect image name from current directory; provide an image name")
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
	key, value, ok = strings.Cut(pair, "=")
	if !ok {
		return "", "", false
	}
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

func buildAndPush(ctx context.Context, out io.Writer, ops pushImageOps, build buildConfig, img imagePush) error {
	if _, err := os.Stat(build.Dockerfile); os.IsNotExist(err) {
		return fmt.Errorf("dockerfile not found: %s", build.Dockerfile)
	}

	// Build and load into local daemon (NOT --push).
	// The native registry push client handles chunked uploads to stay
	// within Cloudflare's 100MB per-request limit.
	if err := cliWriteLine(out, "\nBuilding image..."); err != nil {
		return err
	}
	if err := ops.Build(ctx, buildImageArgs(ctx, img.Version, build.Platform, build.Dockerfile, build.BuildArgs, img.VersionRef, img.LatestRef)); err != nil {
		return err
	}

	if err := cliWriteLine(out, "Pushing..."); err != nil {
		return err
	}
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

func tagAndPush(ctx context.Context, out io.Writer, ops pushImageOps, img imagePush) error {
	localImage := img.SourceRef
	if localImage == "" {
		return errors.New("push source image reference cannot be empty")
	}

	if err := cliWriteLine(out, "\nChecking local image..."); err != nil {
		return err
	}
	exists, err := ops.Exists(ctx, localImage)
	if err != nil {
		return fmt.Errorf("failed to inspect local image %s: %w", localImage, err)
	}
	if !exists {
		return fmt.Errorf("local image %s not found; build and tag it before pushing", localImage)
	}

	if err := cliWriteLine(out, "Tagging..."); err != nil {
		return err
	}
	if err := ops.Tag(ctx, localImage, img.VersionRef); err != nil {
		return fmt.Errorf("failed to tag %s: %w", img.VersionRef, err)
	}
	if img.Version != "latest" {
		if err := ops.Tag(ctx, localImage, img.LatestRef); err != nil {
			return fmt.Errorf("failed to tag %s: %w", img.LatestRef, err)
		}
	}

	if err := cliWriteLine(out, "Pushing..."); err != nil {
		return err
	}
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
	if idx := strings.LastIndex(nameTag, "@"); idx != -1 {
		return registry, nameTag[:idx], nameTag[idx+1:]
	}
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
