package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/domain"
)

func newPinCmd() *cobra.Command {
	var targetTag string

	cmd := &cobra.Command{
		Use:   "pin <domain>",
		Short: "Pin a route to a specific image tag",
		Long: `Lists available image tags for a domain and deploys the selected version.
Tags are read from the Gordon registry.

Examples:
  gordon pin myapp.example.com --remote ...
  gordon pin myapp.example.com --tag v1.0.0 --remote ...
  gordon pin list myapp.example.com --remote ...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPin(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], targetTag)
		},
	}

	cmd.Flags().StringVar(&targetTag, "tag", "", "Target tag (skips interactive selection)")
	cmd.AddCommand(newPinListCmd())

	return cmd
}

func newPinListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list <domain>",
		Short: "List available tags",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPinList(cmd.Context(), cmd.OutOrStdout(), args[0], jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func runPin(ctx context.Context, out, ew io.Writer, pinDomain, targetTag string) error {
	handle, err := resolveControlPlaneForRouteDomain(ctx, pinDomain)
	if err != nil {
		return fmt.Errorf("failed to resolve control plane: %w", err)
	}
	defer handle.close()

	route, err := handle.plane.GetRoute(ctx, pinDomain)
	if err != nil {
		return fmt.Errorf("failed to get route: %w", err)
	}

	_, imageName, currentTag := parseImageRef(route.Image)
	if imageName == "" {
		return fmt.Errorf("cannot parse image name from route: %s", route.Image)
	}

	tags, err := fetchAndSortTags(ctx, handle.plane, imageName)
	if err != nil {
		return fmt.Errorf("failed to fetch tags: %w", err)
	}

	selectedTag, err := selectTag(targetTag, tags, currentTag, pinDomain, out)
	if err != nil {
		return fmt.Errorf("failed to select tag: %w", err)
	}
	if selectedTag == "" {
		return nil
	}

	if selectedTag == currentTag {
		return cliWriteLine(out, styles.RenderWarning(fmt.Sprintf("Already running %s", selectedTag)))
	}

	return deploySelectedTag(ctx, handle.plane, route, out, ew, pinDomain, imageName, selectedTag)
}

func runPinList(ctx context.Context, w io.Writer, pinDomain string, jsonOut bool) error {
	handle, err := resolveControlPlaneForRouteDomain(ctx, pinDomain)
	if err != nil {
		return fmt.Errorf("failed to resolve control plane: %w", err)
	}
	defer handle.close()

	route, err := handle.plane.GetRoute(ctx, pinDomain)
	if err != nil {
		return fmt.Errorf("failed to get route: %w", err)
	}

	_, imageName, currentTag := parseImageRef(route.Image)
	if imageName == "" {
		return fmt.Errorf("cannot parse image name from route: %s", route.Image)
	}

	tags, err := fetchAndSortTags(ctx, handle.plane, imageName)
	if err != nil {
		return fmt.Errorf("failed to fetch tags: %w", err)
	}

	return printPinTags(w, pinDomain, currentTag, tags, jsonOut)
}

func printPinTags(w io.Writer, pinDomain, currentTag string, tags []string, jsonOut bool) error {
	if jsonOut {
		return writeJSON(w, map[string]any{
			"domain":      pinDomain,
			"current_tag": currentTag,
			"tags":        tags,
		})
	}

	if _, err := fmt.Fprintf(w, "Available tags for %s:\n", styles.Theme.Bold.Render(pinDomain)); err != nil {
		return err
	}
	for _, tag := range tags {
		suffix := ""
		if tag == currentTag {
			suffix = " (current)"
		}
		if _, err := fmt.Fprintf(w, "- %s%s\n", tag, suffix); err != nil {
			return err
		}
	}
	return nil
}

func fetchAndSortTags(ctx context.Context, cp ControlPlane, imageName string) ([]string, error) {
	tags, err := cp.ListTags(ctx, imageName)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("no tags found for %s", imageName)
	}

	return sortSemverTags(tags), nil
}

// sortSemverTags normalizes, classifies, and sorts tags.
// Semver tags (with or without 'v' prefix) are sorted in descending order first,
// followed by non-semver tags in descending lexicographic order.
func sortSemverTags(tags []string) []string {
	var semverTags, otherTags []string
	for _, tag := range tags {
		sv := tag
		if !strings.HasPrefix(sv, "v") {
			sv = "v" + sv
		}
		if semver.IsValid(sv) {
			semverTags = append(semverTags, tag)
		} else {
			otherTags = append(otherTags, tag)
		}
	}

	// Sort semver tags in descending order (latest first)
	sort.Slice(semverTags, func(i, j int) bool {
		si, sj := semverTags[i], semverTags[j]
		if !strings.HasPrefix(si, "v") {
			si = "v" + si
		}
		if !strings.HasPrefix(sj, "v") {
			sj = "v" + sj
		}
		return semver.Compare(si, sj) > 0
	})

	// Sort non-semver tags in descending lexicographic order
	sort.Slice(otherTags, func(i, j int) bool {
		return otherTags[i] > otherTags[j]
	})

	// Combine: semver tags first, then other tags
	return append(semverTags, otherTags...)
}

func selectTag(targetTag string, tags []string, currentTag, pinDomain string, out io.Writer) (string, error) {
	if targetTag != "" {
		if !validateTagExists(targetTag, tags) {
			return "", fmt.Errorf("tag %s not found. Available: %s", targetTag, strings.Join(tags, ", "))
		}
		return targetTag, nil
	}

	selectedTag, err := components.RunSelector(
		fmt.Sprintf("Select version for %s:", pinDomain),
		tags,
		currentTag,
	)
	if err != nil {
		return "", err
	}
	if selectedTag == "" {
		_ = cliWriteLine(out, "Cancelled.")
	}
	return selectedTag, nil
}

func validateTagExists(targetTag string, tags []string) bool {
	return slices.Contains(tags, targetTag)
}

func deploySelectedTag(ctx context.Context, cp ControlPlane, route *domain.Route, out, ew io.Writer, pinDomain, imageName, selectedTag string) error {
	registry, _, _ := parseImageRef(route.Image)
	oldImage := route.Image
	route.Image = fmt.Sprintf("%s/%s:%s", registry, imageName, selectedTag)

	if err := cliWritef(out, "Pinning to %s...\n", styles.Theme.Bold.Render(selectedTag)); err != nil {
		return err
	}

	if err := cp.UpdateRoute(ctx, *route); err != nil {
		return fmt.Errorf("failed to update route: %w", err)
	}

	result, err := cp.Deploy(ctx, pinDomain)
	if err != nil {
		// Attempt to revert route to previous image
		route.Image = oldImage
		if revertErr := cp.UpdateRoute(ctx, *route); revertErr != nil {
			_ = cliWritef(ew, "WARNING: deploy failed and could not revert route: %v\n", revertErr)
			return fmt.Errorf("failed to deploy; revert failed: %v; deploy error: %w", revertErr, err)
		}
		return fmt.Errorf("failed to deploy (route reverted): %w", err)
	}
	containerID := shortContainerID(result.ContainerID)
	return cliWriteLine(out, styles.RenderSuccess(fmt.Sprintf("Pinned %s to %s (container: %s)", pinDomain, selectedTag, containerID)))
}
