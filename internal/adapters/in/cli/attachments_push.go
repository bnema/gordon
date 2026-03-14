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
	tagAndPushFn          = tagAndPush
	buildAndPushFn        = buildAndPush
	determineVersionFn    = determineVersion
)

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
			return runAttachmentsPush(cmd.Context(), args[0], tag, build, platform, dockerfile, buildArgs, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&build, "build", false, "Build the image first using docker buildx")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform (used with --build)")
	cmd.Flags().StringVarP(&dockerfile, "file", "f", "", "Path to Dockerfile (default: ./Dockerfile, used with --build)")
	cmd.Flags().StringVar(&tag, "tag", "", "Override version tag (default: CI tag ref or git describe)")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", nil, "Additional build args (used with --build)")

	return cmd
}

func runAttachmentsPush(ctx context.Context, imageArg, tag string, build bool, platform string, dockerfile string, buildArgs []string, out io.Writer) error {
	handle, err := resolveControlPlaneFn(configPath)
	if err != nil {
		return err
	}
	defer handle.close()

	dockerfile, err = resolveDockerfile(dockerfile, build)
	if err != nil {
		return err
	}

	for _, ba := range buildArgs {
		if err := validateBuildArg(ba); err != nil {
			return err
		}
	}

	registry, imageName, targets, err := resolveAttachmentImage(ctx, handle.plane, imageArg)
	if err != nil {
		return err
	}

	version, err := resolveVersionWithFn(ctx, tag)
	if err != nil {
		return err
	}

	versionRef, latestRef := resolveImageRefs(registry, imageName, version)

	cliWritef(out, "Attachment image: %s\n", styles.Theme.Bold.Render(imageArg))
	cliWritef(out, "Targets:          %s\n", styles.Theme.Bold.Render(formatAttachmentTargets(targets)))
	cliWritef(out, "Image:            %s\n", styles.Theme.Bold.Render(versionRef))
	if version != "latest" {
		cliWritef(out, "Also:             %s\n", styles.Theme.Bold.Render(latestRef))
	}

	if build {
		if err := buildAndPushFn(ctx, version, platform, dockerfile, buildArgs, versionRef, latestRef); err != nil {
			return err
		}
	} else {
		if err := tagAndPushFn(ctx, registry, imageName, version, versionRef, latestRef); err != nil {
			return err
		}
	}

	cliWriteLine(out, styles.RenderSuccess("Push complete"))
	return nil
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
