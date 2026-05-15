package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/pkg/validation"
)

var (
	resolveControlPlaneFn = resolveControlPlane
	determineVersionFn    = determineVersion
)

// attachmentPushRequest holds all inputs for the attachments push command.
type attachmentPushRequest struct {
	ImageArg string
	Tag      string
	Build    buildConfig
}

func newAttachmentsPushCmd() *cobra.Command {
	var (
		build      bool
		platform   string
		tag        string
		dockerfile string
		buildArgs  []string
	)

	cmd := &cobra.Command{
		Use:   "push <image>",
		Short: "Build, tag, and push an attachment image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttachmentsPush(cmd.Context(), attachmentPushRequest{
				ImageArg: args[0],
				Tag:      tag,
				Build:    buildConfig{Enabled: build, Platform: platform, Dockerfile: dockerfile, BuildArgs: buildArgs},
			}, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&build, "build", false, "Build the image first using docker buildx")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform (used with --build)")
	cmd.Flags().StringVarP(&dockerfile, "file", "f", "", "Path to Dockerfile (default: ./Dockerfile, used with --build)")
	cmd.Flags().StringVar(&tag, "tag", "", "Override version tag (default: CI tag ref or git describe)")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Additional build args (used with --build)")

	return cmd
}

func runAttachmentsPush(ctx context.Context, req attachmentPushRequest, out io.Writer) error {
	dockerfile, err := resolveDockerfile(req.Build.Dockerfile, req.Build.Enabled)
	if err != nil {
		return err
	}

	inferredRemote, err := inferRemoteForAttachmentImage(ctx, req.ImageArg)
	if err != nil {
		return err
	}

	var handle *controlPlaneHandle
	if inferredRemote != nil {
		handle = newRemoteControlPlaneHandle(inferredRemote)
		if err := cliWritef(out, "Remote:           %s %s\n", styles.Theme.Bold.Render(inferredRemote.DisplayName()), styles.Theme.Muted.Render("(auto-detected)")); err != nil {
			return err
		}
	} else {
		handle, err = resolveControlPlaneFn(configPath)
		if err != nil {
			return err
		}
	}
	defer handle.close()

	for _, ba := range req.Build.BuildArgs {
		if err := validateBuildArg(ba); err != nil {
			return err
		}
	}

	registry, imageName, targets, err := resolveAttachmentImage(ctx, handle.plane, req.ImageArg)
	if err != nil {
		return err
	}

	version, err := resolveVersionWithFn(ctx, req.Tag)
	if err != nil {
		return err
	}

	img := imagePush{
		Registry:  registry,
		ImageName: imageName,
		Version:   version,
	}
	img.VersionRef, img.LatestRef = resolveImageRefs(registry, imageName, version)

	var imageOps pushImageOps
	if inferredRemote != nil {
		imageOps, err = newImageOpsForResolvedRemote(inferredRemote)
	} else {
		imageOps, err = newImageOpsFn()
	}
	if err != nil {
		return err
	}

	if err := printAttachmentPushInfo(out, req.ImageArg, targets, img.VersionRef, img.LatestRef, version); err != nil {
		return err
	}

	build := buildConfig{
		Enabled:    req.Build.Enabled,
		Platform:   req.Build.Platform,
		Dockerfile: dockerfile,
		BuildArgs:  req.Build.BuildArgs,
	}

	if err := performAttachmentPush(ctx, imageOps, build, img); err != nil {
		return err
	}

	return cliWriteLine(out, styles.RenderSuccess("Push complete"))
}

func printAttachmentPushInfo(out io.Writer, imageArg string, targets []string, versionRef, latestRef, version string) error {
	if err := cliWritef(out, "Attachment image: %s\n", styles.Theme.Bold.Render(imageArg)); err != nil {
		return err
	}
	if err := cliWritef(out, "Targets:          %s\n", styles.Theme.Bold.Render(formatAttachmentTargets(targets))); err != nil {
		return err
	}
	if err := cliWritef(out, "Image:            %s\n", styles.Theme.Bold.Render(versionRef)); err != nil {
		return err
	}
	if version != "latest" {
		if err := cliWritef(out, "Also:             %s\n", styles.Theme.Bold.Render(latestRef)); err != nil {
			return err
		}
	}
	return nil
}

func performAttachmentPush(ctx context.Context, ops pushImageOps, build buildConfig, img imagePush) error {
	if build.Enabled {
		return buildAndPush(ctx, ops, build, img)
	}
	return tagAndPush(ctx, ops, img)
}

func resolveAttachmentImage(ctx context.Context, cp ControlPlane, imageArg string) (registry, imageName string, targets []string, err error) {
	imageName = normalizeAttachmentImageName(imageArg)

	targets, err = cp.FindAttachmentTargetsByImage(ctx, imageArg)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to find attachment targets for image %q: %w", imageArg, err)
	}
	if len(targets) == 0 {
		return "", "", nil, fmt.Errorf("image %q is not configured as an attachment", imageArg)
	}

	status, err := cp.GetStatus(ctx)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to get status: %w", err)
	}

	return status.RegistryDomain, imageName, targets, nil
}

func normalizeAttachmentImageName(imageArg string) string {
	name := imageArg
	if idx := strings.Index(name, "@"); idx != -1 {
		name = name[:idx]
	}
	if idx := strings.LastIndex(name, ":"); idx != -1 {
		slashIdx := strings.LastIndex(name, "/")
		if idx > slashIdx {
			name = name[:idx]
		}
	}
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		host := parts[0]
		if strings.ContainsAny(host, ".:") || host == "localhost" {
			return parts[1]
		}
	}
	return name
}

func formatAttachmentTargets(targets []string) string {
	cloned := append([]string(nil), targets...)
	sort.Strings(cloned)
	return fmt.Sprintf("%v", cloned)
}

func resolveVersionWithFn(ctx context.Context, tag string) (string, error) {
	version := determineVersionFn(ctx, tag)
	if version != "latest" {
		if err := validateVersionTag(version); err != nil {
			return "", err
		}
	}
	return version, nil
}

func validateVersionTag(version string) error {
	if err := validation.ValidateReference(version); err != nil {
		return fmt.Errorf("invalid version tag %q: %w", version, err)
	}
	return nil
}
