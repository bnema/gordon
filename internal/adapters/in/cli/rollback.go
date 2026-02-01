package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
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
  gordon rollback myapp.example.com --tag v1.0.0 --remote ...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRollback(cmd.Context(), args[0], targetTag)
		},
	}

	cmd.Flags().StringVar(&targetTag, "tag", "", "Target tag (skips interactive selection)")

	return cmd
}

func runRollback(ctx context.Context, rollbackDomain, targetTag string) error {
	client, isRemote := GetRemoteClient()
	if !isRemote {
		fmt.Println(styles.RenderError("rollback command requires --remote flag or GORDON_REMOTE env var"))
		return fmt.Errorf("rollback requires remote mode")
	}

	route, err := client.GetRoute(ctx, rollbackDomain)
	if err != nil {
		return fmt.Errorf("failed to get route: %w", err)
	}

	_, imageName, currentTag := parseImageRef(route.Image)
	if imageName == "" {
		return fmt.Errorf("cannot parse image name from route: %s", route.Image)
	}

	tags, err := fetchAndSortTags(ctx, client, imageName)
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

	return deploySelectedTag(ctx, client, route, rollbackDomain, imageName, selectedTag)
}

func fetchAndSortTags(ctx context.Context, client *remote.Client, imageName string) ([]string, error) {
	tags, err := client.ListTags(ctx, imageName)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("no tags found for %s", imageName)
	}

	// Separate semver tags from non-semver tags
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
	tags = append(semverTags, otherTags...)
	return tags, nil
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

func deploySelectedTag(ctx context.Context, client *remote.Client, route *domain.Route, rollbackDomain, imageName, selectedTag string) error {
	registry, _, _ := parseImageRef(route.Image)
	oldImage := route.Image
	route.Image = fmt.Sprintf("%s/%s:%s", registry, imageName, selectedTag)

	fmt.Printf("Rolling back to %s...\n", styles.Theme.Bold.Render(selectedTag))

	if err := client.UpdateRoute(ctx, *route); err != nil {
		return fmt.Errorf("failed to update route: %w", err)
	}

	result, err := client.Deploy(ctx, rollbackDomain)
	if err != nil {
		// Attempt to revert route to previous image
		route.Image = oldImage
		if revertErr := client.UpdateRoute(ctx, *route); revertErr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: deploy failed and could not revert route: %v\n", revertErr)
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
