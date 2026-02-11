package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/domain"
)

func newRollbackCmd() *cobra.Command {
	var targetTag string

	cmd := &cobra.Command{
		Use:   "rollback <domain>",
		Short: "Roll back to a previous image version",
		Long: `Lists available image tags for a domain and deploys the selected version.
Tags are read from the Gordon registry.

Examples:
  gordon rollback myapp.example.com --remote ...
  gordon rollback myapp.example.com --tag v1.0.0 --remote ...
  gordon rollback list myapp.example.com --remote ...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd.Context(), args[0], targetTag)
		},
	}

	cmd.Flags().StringVar(&targetTag, "tag", "", "Target tag (skips interactive selection)")
	cmd.AddCommand(newRollbackListCmd())

	return cmd
}

func newRollbackListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <domain>",
		Short: "List available rollback tags",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollbackList(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
}

func runRollback(ctx context.Context, rollbackDomain, targetTag string) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()

	route, err := handle.plane.GetRoute(ctx, rollbackDomain)
	if err != nil {
		return fmt.Errorf("failed to get route: %w", err)
	}

	_, imageName, currentTag := parseImageRef(route.Image)
	if imageName == "" {
		return fmt.Errorf("cannot parse image name from route: %s", route.Image)
	}

	tags, err := fetchAndSortTags(ctx, handle.plane, imageName)
	if err != nil {
		return err
	}

	selectedTag, err := selectTag(targetTag, tags, currentTag, rollbackDomain)
	if err != nil || selectedTag == "" {
		return err
	}

	if selectedTag == currentTag {
		fmt.Println(styles.RenderWarning(fmt.Sprintf("Already running %s", selectedTag)))
		return nil
	}

	return deploySelectedTag(ctx, handle.plane, route, rollbackDomain, imageName, selectedTag)
}

func runRollbackList(ctx context.Context, w io.Writer, rollbackDomain string) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()

	route, err := handle.plane.GetRoute(ctx, rollbackDomain)
	if err != nil {
		return fmt.Errorf("failed to get route: %w", err)
	}

	_, imageName, currentTag := parseImageRef(route.Image)
	if imageName == "" {
		return fmt.Errorf("cannot parse image name from route: %s", route.Image)
	}

	tags, err := fetchAndSortTags(ctx, handle.plane, imageName)
	if err != nil {
		return err
	}

	printRollbackTags(w, rollbackDomain, currentTag, tags)
	return nil
}

func printRollbackTags(w io.Writer, rollbackDomain, currentTag string, tags []string) {
	fmt.Fprintf(w, "Available tags for %s:\n", styles.Theme.Bold.Render(rollbackDomain))
	for _, tag := range tags {
		suffix := ""
		if tag == currentTag {
			suffix = " (current)"
		}
		fmt.Fprintf(w, "- %s%s\n", tag, suffix)
	}
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

func selectTag(targetTag string, tags []string, currentTag, rollbackDomain string) (string, error) {
	if targetTag != "" {
		if !validateTagExists(targetTag, tags) {
			return "", fmt.Errorf("tag %s not found. Available: %s", targetTag, strings.Join(tags, ", "))
		}
		return targetTag, nil
	}

	selectedTag, err := components.RunSelector(
		fmt.Sprintf("Select version for %s:", rollbackDomain),
		tags,
		currentTag,
	)
	if err != nil {
		return "", err
	}
	if selectedTag == "" {
		fmt.Println("Cancelled.")
	}
	return selectedTag, nil
}

func validateTagExists(targetTag string, tags []string) bool {
	for _, t := range tags {
		if t == targetTag {
			return true
		}
	}
	return false
}

func deploySelectedTag(ctx context.Context, cp ControlPlane, route *domain.Route, rollbackDomain, imageName, selectedTag string) error {
	registry, _, _ := parseImageRef(route.Image)
	oldImage := route.Image
	route.Image = fmt.Sprintf("%s/%s:%s", registry, imageName, selectedTag)

	fmt.Printf("Rolling back to %s...\n", styles.Theme.Bold.Render(selectedTag))

	if err := cp.UpdateRoute(ctx, *route); err != nil {
		return fmt.Errorf("failed to update route: %w", err)
	}

	result, err := cp.Deploy(ctx, rollbackDomain)
	if err != nil {
		// Attempt to revert route to previous image
		route.Image = oldImage
		if revertErr := cp.UpdateRoute(ctx, *route); revertErr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: deploy failed and could not revert route: %v\n", revertErr)
			return fmt.Errorf("failed to deploy; revert failed: %v; deploy error: %w", revertErr, err)
		}
		return fmt.Errorf("failed to deploy (route reverted): %w", err)
	}
	containerID := result.ContainerID
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}
	fmt.Println(styles.RenderSuccess(fmt.Sprintf("Rolled back %s to %s (container: %s)", rollbackDomain, selectedTag, containerID)))
	return nil
}
