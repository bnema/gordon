package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/domain"
)

type imagesClient interface {
	ListImages(ctx context.Context) ([]dto.Image, error)
	PruneImages(ctx context.Context, req dto.ImagePruneRequest) (*dto.ImagePruneResponse, error)
}

type imagesPruneOptions struct {
	DryRun       bool
	KeepReleases int
	Dangling     bool // scope flag: prune dangling runtime images
	Registry     bool // scope flag: prune registry tags
	NoConfirm    bool
}

type prunePreview struct {
	danglingRuntimeCount int
	registryTagsToPrune  int
}

// pruneConfirmFunc is the confirmation callback used before destructive prune.
// It is a package-level variable so tests can replace it without spawning a
// real Bubble Tea program.
var pruneConfirmFunc = defaultPruneConfirm

func defaultPruneConfirm(prompt string) (bool, error) {
	return components.RunConfirm(prompt, components.WithDefaultYes())
}

// Column widths are fixed to keep table output predictable across terminals.
// Values are chosen for typical repository names and SHA-prefixed IDs;
// the table component truncates with ellipsis when content overflows.
const (
	imagesListRepositoryColumnWidth = 44
	imagesListTagColumnWidth        = 14
	imagesListSizeColumnWidth       = 10
	imagesListCreatedColumnWidth    = 22
	imagesListImageIDColumnWidth    = 22
	imagesListDanglingColumnWidth   = 10
)

var imagesListTableColumns = []components.TableColumn{
	{Title: "REPOSITORY", Width: imagesListRepositoryColumnWidth},
	{Title: "TAG", Width: imagesListTagColumnWidth},
	{Title: "SIZE", Width: imagesListSizeColumnWidth},
	{Title: "CREATED", Width: imagesListCreatedColumnWidth},
	{Title: "IMAGE_ID", Width: imagesListImageIDColumnWidth},
	{Title: "DANGLING", Width: imagesListDanglingColumnWidth},
}

func newImagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "images",
		Short: "List and prune images",
		Long: `Inspect and clean up runtime and registry images.

These commands currently require remote mode with a configured target.`,
	}

	cmd.AddCommand(newImagesListCmd())
	cmd.AddCommand(newImagesPruneCmd())

	return cmd
}

func newImagesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List runtime images and registry tags",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("images commands require a configured remote target")
			}

			return runImagesList(cmd.Context(), client, cmd.OutOrStdout())
		},
	}
}

func newImagesPruneCmd() *cobra.Command {
	var opts imagesPruneOptions

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune unused images",
		Long: `Remove dangling runtime images and/or old registry tags.

By default both runtime and registry cleanup run, keeping latest + 3 previous
release tags per repository. Use --dangling or --registry to restrict scope.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("images commands require a configured remote target")
			}

			return runImagesPrune(cmd.Context(), client, opts, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Show what would be pruned without applying changes")
	cmd.Flags().IntVar(&opts.KeepReleases, "keep-releases", domain.DefaultImagePruneKeepLast,
		"Number of previous non-latest tags to keep per repository (latest is always kept)")
	cmd.Flags().BoolVar(&opts.Dangling, "dangling", false, "Include dangling runtime images (default: both scopes)")
	cmd.Flags().BoolVar(&opts.Registry, "registry", false, "Include old registry tags (default: both scopes)")
	cmd.Flags().BoolVar(&opts.NoConfirm, "no-confirm", false, "Skip confirmation prompt")

	return cmd
}

// resolvePruneScopes determines which prune subsystems are active.
// No scope flags → both true. Any scope flag present → only selected scopes.
func resolvePruneScopes(opts imagesPruneOptions) (pruneDangling, pruneRegistry bool) {
	if !opts.Dangling && !opts.Registry {
		return true, true
	}
	return opts.Dangling, opts.Registry
}

func pruneWithSpinner(ctx context.Context, client imagesClient, req dto.ImagePruneRequest, pruneDangling, pruneRegistry bool) (*dto.ImagePruneResponse, error) {
	if !isInteractiveTerminal() {
		return client.PruneImages(ctx, req)
	}

	type pruneResult struct {
		resp *dto.ImagePruneResponse
		err  error
	}

	done := make(chan pruneResult, 1)
	go func() {
		resp, err := client.PruneImages(ctx, req)
		done <- pruneResult{resp: resp, err: err}
	}()

	// Build spinner message based on scopes
	msg := "Pruning images"
	if pruneDangling && pruneRegistry {
		msg = "Pruning runtime and registry images"
	} else if pruneDangling {
		msg = "Pruning runtime images"
	} else if pruneRegistry {
		msg = "Pruning registry images"
	}

	model := components.NewSpinner(
		components.WithMessage(msg),
		components.WithSpinnerType(components.SpinnerMiniDot),
	)

	for {
		select {
		case result := <-done:
			if result.err != nil {
				return nil, result.err
			}
			if result.resp == nil {
				return nil, fmt.Errorf("prune operation completed but no response received")
			}
			// Success - set final message and render
			model.SetMessage(cliRenderSuccess("Pruning complete"))
			updatedModel, _ := model.Update(spinner.TickMsg{})
			if m, ok := updatedModel.(components.SpinnerModel); ok {
				fmt.Print("\r" + m.View() + "\n")
			}
			return result.resp, nil
		case <-time.After(100 * time.Millisecond):
			updatedModel, _ := model.Update(spinner.TickMsg{})
			if m, ok := updatedModel.(components.SpinnerModel); ok {
				model = m
				fmt.Print("\r" + m.View())
			}
		}
	}
}

func runImagesList(ctx context.Context, client imagesClient, out io.Writer) error {
	images, err := client.ListImages(ctx)
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	if len(images) == 0 {
		if err := cliWriteLine(out, cliRenderMuted("No images found")); err != nil {
			return err
		}
		err := cliWriteLine(out, cliRenderInfo("Total images: 0 (dangling: 0)"))
		return err
	}

	if err := cliWriteLine(out, cliRenderTitle("Images")); err != nil {
		return err
	}

	danglingCount := 0
	rows := make([][]string, 0, len(images))
	for _, img := range images {
		dangling := "no"
		if img.Dangling {
			dangling = "yes"
			danglingCount++
		}

		rows = append(rows, []string{
			img.Repository,
			img.Tag,
			formatImageSize(img.Size),
			formatImageCreatedAt(img.Created),
			formatImageID(img.ID),
			dangling,
		})
	}

	table := components.NewTable(
		components.WithColumns(imagesListTableColumns),
		components.WithRows(rows),
		components.WithHeaderStyle(lipgloss.NewStyle().Bold(true)),
		components.WithCellStyle(lipgloss.NewStyle()),
	)

	if err := cliWriteLine(out, table.View()); err != nil {
		return err
	}

	err = cliWritef(out, "\nTotal images: %d (dangling: %d)\n", len(images), danglingCount)
	return err
}

func runImagesPrune(ctx context.Context, client imagesClient, opts imagesPruneOptions, out io.Writer) error {
	if opts.KeepReleases < 0 {
		return fmt.Errorf("--keep-releases must be >= 0")
	}

	pruneDangling, pruneRegistry := resolvePruneScopes(opts)

	if opts.DryRun {
		return runImagesPruneDryRun(ctx, client, opts, pruneDangling, pruneRegistry, out)
	}

	// Confirmation prompt for destructive operations.
	if !opts.NoConfirm {
		preview, err := loadPrunePreview(ctx, client, opts.KeepReleases, pruneDangling, pruneRegistry)
		if err != nil {
			return fmt.Errorf("failed to load prune preview: %w", err)
		}

		confirmed, err := pruneConfirmFunc(renderPruneConfirmPrompt(preview, opts.KeepReleases, pruneDangling, pruneRegistry))
		if err != nil {
			return fmt.Errorf("confirmation failed: %w", err)
		}
		if !confirmed {
			return cliWriteLine(out, cliRenderMuted("Prune cancelled"))
		}
	}

	req := buildPruneRequest(opts.KeepReleases, pruneDangling, pruneRegistry)

	resp, err := pruneWithSpinner(ctx, client, req, pruneDangling, pruneRegistry)
	if err != nil {
		return fmt.Errorf("failed to prune images: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("failed to prune images: empty response")
	}

	if pruneDangling {
		if err := cliWritef(out, "Runtime: deleted=%d space_reclaimed=%d\n", resp.Runtime.DeletedCount, resp.Runtime.SpaceReclaimed); err != nil {
			return err
		}
	} else {
		if err := cliWriteLine(out, cliRenderMuted("Runtime cleanup skipped (--registry)")); err != nil {
			return err
		}
	}

	if pruneRegistry {
		err = cliWritef(out, "Registry: tags_removed=%d blobs_removed=%d space_reclaimed=%d\n", resp.Registry.TagsRemoved, resp.Registry.BlobsRemoved, resp.Registry.SpaceReclaimed)
		return err
	}

	err = cliWriteLine(out, cliRenderMuted("Registry cleanup skipped (--dangling)"))
	return err
}

func runImagesPruneDryRun(ctx context.Context, client imagesClient, opts imagesPruneOptions, pruneDangling, pruneRegistry bool, out io.Writer) error {
	if err := cliWriteLine(out, cliRenderWarning("Dry run: no changes applied")); err != nil {
		return err
	}

	if pruneDangling {
		images, err := client.ListImages(ctx)
		if err != nil {
			return fmt.Errorf("failed to list images: %w", err)
		}

		danglingCount := 0
		for _, img := range images {
			if img.Dangling {
				danglingCount++
			}
		}

		if err := cliWritef(out, "Runtime: would prune %d dangling runtime images\n", danglingCount); err != nil {
			return err
		}
	} else {
		if err := cliWriteLine(out, cliRenderMuted("Runtime cleanup skipped (--registry)")); err != nil {
			return err
		}
	}

	if pruneRegistry {
		return cliWritef(out, "Registry: would keep latest + %d previous tags per repository\n", opts.KeepReleases)
	}

	return cliWriteLine(out, cliRenderMuted("Registry cleanup skipped (--dangling)"))
}

func loadPrunePreview(ctx context.Context, client imagesClient, keepReleases int, pruneDangling, pruneRegistry bool) (prunePreview, error) {
	images, err := client.ListImages(ctx)
	if err != nil {
		return prunePreview{}, fmt.Errorf("failed to list images: %w", err)
	}

	preview := prunePreview{}
	if pruneDangling {
		for _, img := range images {
			if img.Dangling {
				preview.danglingRuntimeCount++
			}
		}
	}

	if pruneRegistry {
		preview.registryTagsToPrune = estimateRegistryTagsToPrune(images, keepReleases)
	}

	return preview, nil
}

func renderPruneConfirmPrompt(preview prunePreview, keepReleases int, pruneDangling, pruneRegistry bool) string {
	lines := []string{"Prune images? This cannot be undone.", ""}

	if pruneDangling {
		lines = append(lines, fmt.Sprintf("Runtime: would prune %d dangling runtime images", preview.danglingRuntimeCount))
	} else {
		lines = append(lines, "Runtime: cleanup skipped (--registry)")
	}

	if pruneRegistry {
		lines = append(lines, fmt.Sprintf(
			"Registry: would prune %d tags (keep latest + %d previous tags per repository)",
			preview.registryTagsToPrune,
			keepReleases,
		))
	} else {
		lines = append(lines, "Registry: cleanup skipped (--dangling)")
	}

	return strings.Join(lines, "\n")
}

type previewRegistryTag struct {
	name    string
	created time.Time
}

func estimateRegistryTagsToPrune(images []dto.Image, keepReleases int) int {
	tagsByRepo := collectTagsByRepository(images)
	removed := 0

	for _, repoTags := range tagsByRepo {
		removed += countTagsToRemove(repoTags, keepReleases)
	}

	return removed
}

// collectTagsByRepository groups images by repository and returns
// map of repo -> tag name -> creation time
func collectTagsByRepository(images []dto.Image) map[string]map[string]time.Time {
	tagsByRepo := make(map[string]map[string]time.Time)

	for _, img := range images {
		repo := strings.TrimSpace(img.Repository)
		tag := strings.TrimSpace(img.Tag)
		if repo == "" || tag == "" || repo == "<none>" || tag == "<none>" {
			continue
		}

		if _, ok := tagsByRepo[repo]; !ok {
			tagsByRepo[repo] = make(map[string]time.Time)
		}

		repoTags := tagsByRepo[repo]
		if current, exists := repoTags[tag]; !exists || img.Created.After(current) {
			repoTags[tag] = img.Created
		}
	}

	return tagsByRepo
}

// countTagsToRemove counts how many tags in a repository would be removed
// based on keepReleases retention policy
func countTagsToRemove(repoTags map[string]time.Time, keepReleases int) int {
	tagInfos := toSortedTagInfos(repoTags)

	kept := selectKeptTags(tagInfos, keepReleases)

	return len(tagInfos) - len(kept)
}

// toSortedTagInfos converts repo tags map to sorted slice
func toSortedTagInfos(repoTags map[string]time.Time) []previewRegistryTag {
	tagInfos := make([]previewRegistryTag, 0, len(repoTags))
	for name, created := range repoTags {
		tagInfos = append(tagInfos, previewRegistryTag{name: name, created: created})
	}

	sort.Slice(tagInfos, func(i, j int) bool {
		if tagInfos[i].created.Equal(tagInfos[j].created) {
			return tagInfos[i].name > tagInfos[j].name
		}
		return tagInfos[i].created.After(tagInfos[j].created)
	})

	return tagInfos
}

// selectKeptTags returns which tags would be kept based on
// retention policy (latest + keepReleases non-latest)
func selectKeptTags(tagInfos []previewRegistryTag, keepReleases int) map[string]struct{} {
	kept := make(map[string]struct{})

	// Always keep latest if present
	for _, info := range tagInfos {
		if info.name == "latest" {
			kept["latest"] = struct{}{}
			break
		}
	}

	// Keep most recent non-latest tags
	keptCount := 0
	for _, info := range tagInfos {
		if info.name == "latest" {
			continue
		}
		if keptCount >= keepReleases {
			break
		}
		kept[info.name] = struct{}{}
		keptCount++
	}

	return kept
}

func buildPruneRequest(keepReleases int, pruneDangling, pruneRegistry bool) dto.ImagePruneRequest {
	return dto.ImagePruneRequest{
		KeepLast:      &keepReleases,
		PruneDangling: &pruneDangling,
		PruneRegistry: &pruneRegistry,
	}
}

func formatImageCreatedAt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func formatImageSize(size int64) string {
	if size <= 0 {
		return "-"
	}

	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatImageID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "-"
	}

	return id
}
