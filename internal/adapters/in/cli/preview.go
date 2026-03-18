package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auto/preview"
)

func newPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview [name]",
		Short: "Create or manage preview environments",
		Long:  "Build, push, and deploy an ephemeral preview environment. Defaults to current git branch name.",
	}

	createCmd := newPreviewCreateCmd()

	cmd.AddCommand(
		createCmd,
		newPreviewListCmd(),
		newPreviewDeleteCmd(),
		newPreviewExtendCmd(),
	)

	return cmd
}

func newPreviewCreateCmd() *cobra.Command {
	var (
		ttl      string
		noBuild  bool
		noData   bool
		platform string
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a preview environment",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// Resolve preview name from arg or git branch.
			name, err := resolvePreviewName(ctx, args)
			if err != nil {
				return err
			}

			previewTag := "preview-" + name

			if err := cliWriteLine(out, cliRenderTitle("Preview: "+name)); err != nil {
				return err
			}

			// Resolve control plane (remote or local) — same as push.
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			previewRef, err := resolvePreviewImageRef(ctx, handle.plane, out, previewTag)
			if err != nil {
				return err
			}

			if err := buildAndPushPreview(ctx, out, previewRef, previewTag, platform, noBuild); err != nil {
				return err
			}

			return waitForPreview(ctx, out, name, ttl, noData)
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "", "Override TTL (e.g., 72h)")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "Skip image build")
	cmd.Flags().BoolVar(&noData, "no-data", false, "Skip volume cloning")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform for build")
	return cmd
}

func newPreviewListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active preview environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("preview list requires --remote (local preview listing is not yet supported)")
			}

			previews, err := client.ListPreviews(ctx)
			if err != nil {
				return fmt.Errorf("failed to list previews: %w", err)
			}

			if jsonOutput {
				return writeJSON(out, previews)
			}

			if len(previews) == 0 {
				return cliWriteLine(out, cliRenderEmptyState("No active previews"))
			}

			return printPreviewTable(out, previews)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newPreviewDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a preview environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			name := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("preview delete requires --remote (local preview deletion is not yet supported)")
			}

			if err := client.DeletePreview(ctx, name); err != nil {
				return fmt.Errorf("failed to delete preview %q: %w", name, err)
			}

			return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Preview %q deleted", name)))
		},
	}
}

func newPreviewExtendCmd() *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "extend <name>",
		Short: "Extend a preview environment's TTL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			name := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("preview extend requires --remote (local preview extend is not yet supported)")
			}

			if err := client.ExtendPreview(ctx, name, ttl); err != nil {
				return fmt.Errorf("failed to extend preview %q: %w", name, err)
			}

			return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Preview %q extended by %s", name, ttl)))
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "Additional TTL duration")
	return cmd
}

func printPreviewTable(out io.Writer, previews []domain.PreviewRoute) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tDOMAIN\tIMAGE\tCREATED\tEXPIRES"); err != nil {
		return err
	}
	now := time.Now()
	for _, p := range previews {
		remaining := time.Until(p.ExpiresAt).Truncate(time.Minute)
		expiresStr := remaining.String()
		if p.IsExpired(now) {
			expiresStr = "expired"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			p.Name,
			p.Domain,
			truncateImage(p.Image, 40),
			p.CreatedAt.Format(time.DateTime),
			expiresStr,
		); err != nil {
			return err
		}
	}
	return w.Flush()
}

func resolvePreviewName(ctx context.Context, args []string) (string, error) {
	if len(args) > 0 {
		name := preview.SanitizeBranchName(args[0])
		if name == "" {
			return "", fmt.Errorf("invalid preview name after sanitization: %q", args[0])
		}
		return name, nil
	}
	branch, err := detectGitBranch(ctx)
	if err != nil {
		return "", fmt.Errorf("no preview name provided and could not detect git branch: %w", err)
	}
	name := preview.SanitizeBranchName(branch)
	if name == "" {
		return "", fmt.Errorf("git branch %q produced empty preview name after sanitization", branch)
	}
	return name, nil
}

func resolvePreviewImageRef(ctx context.Context, cp ControlPlane, out io.Writer, previewTag string) (string, error) {
	dockerfile := "Dockerfile"
	imageName, err := detectImageName(dockerfile)
	if err != nil {
		return "", err
	}
	if err := cliWriteLine(out, cliRenderMeta("Image:", imageName)); err != nil {
		return "", err
	}

	registry, _, _, err := resolveRoute(ctx, cp, "", "", dockerfile)
	if err != nil {
		return "", fmt.Errorf("failed to resolve registry: %w", err)
	}

	previewRef := fmt.Sprintf("%s/%s:%s", registry, imageName, previewTag)
	if err := cliWriteLine(out, cliRenderMeta("Tag:", previewRef)); err != nil {
		return "", err
	}
	return previewRef, nil
}

func buildAndPushPreview(ctx context.Context, out io.Writer, previewRef, previewTag, platform string, noBuild bool) error {
	imageOps, err := newImageOpsFn()
	if err != nil {
		return err
	}

	if !noBuild {
		dockerfile := "Dockerfile"
		if _, statErr := os.Stat(dockerfile); os.IsNotExist(statErr) {
			return fmt.Errorf("dockerfile not found: %s", dockerfile)
		}
		buildArgs := buildImageArgs(ctx, previewTag, platform, dockerfile, nil, previewRef, previewRef)
		if err := cliWriteLine(out, "\nBuilding image..."); err != nil {
			return err
		}
		if err := imageOps.Build(ctx, buildArgs); err != nil {
			return err
		}
	}

	if err := cliWriteLine(out, "Pushing..."); err != nil {
		return err
	}
	if err := imageOps.Push(ctx, previewRef); err != nil {
		return fmt.Errorf("failed to push %s: %w", previewRef, err)
	}
	return nil
}

func printPreviewFlags(out io.Writer, ttl string, noData bool) error {
	if ttl != "" {
		if err := cliWritef(out, "Requested TTL: %s (server config controls actual TTL)\n", ttl); err != nil {
			return err
		}
	}
	if noData {
		if err := cliWriteLine(out, cliRenderInfo("Data copy: skipped (--no-data)")); err != nil {
			return err
		}
	}
	return nil
}

func waitForPreview(ctx context.Context, out io.Writer, name, ttl string, noData bool) error {
	if err := printPreviewFlags(out, ttl, noData); err != nil {
		return err
	}

	client, isRemote := GetRemoteClient()
	if !isRemote {
		return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Push complete. Preview %q will be created by the server (use --remote to poll status).", name)))
	}

	result, err := pollPreviewWithSpinner(ctx, client, name)
	if err != nil {
		return err
	}

	switch result.Status {
	case domain.PreviewStatusRunning:
		scheme := "http"
		if result.HTTPS {
			scheme = "https"
		}
		previewURL := fmt.Sprintf("%s://%s", scheme, result.Domain)
		if err := cliWriteLine(out, cliRenderSuccess("Preview deployed")); err != nil {
			return err
		}
		return cliWriteLine(out, cliRenderMeta("URL:", previewURL))
	case domain.PreviewStatusFailed:
		return fmt.Errorf("preview deployment failed")
	default:
		return cliWriteLine(out, cliRenderInfo("Preview is still deploying. Check status with: gordon preview list --remote"))
	}
}

type previewPollResult struct {
	preview *domain.PreviewRoute
	err     error
}

type previewPollDoneMsg previewPollResult

type previewSpinnerModel struct {
	spinner  components.SpinnerModel
	done     <-chan previewPollResult
	outcome  previewPollResult
	finished bool
}

func newPreviewSpinnerModel(name string, done <-chan previewPollResult) previewSpinnerModel {
	return previewSpinnerModel{
		spinner: components.NewSpinner(
			components.WithMessage(fmt.Sprintf("Deploying preview %s...", name)),
			components.WithSpinnerType(components.SpinnerMiniDot),
		),
		done: done,
	}
}

func (m previewSpinnerModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Init(), waitForPreviewPollDone(m.done))
}

func (m previewSpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case previewPollDoneMsg:
		m.outcome = previewPollResult(msg)
		m.finished = true
		return m, tea.Quit
	default:
		updated, cmd := m.spinner.Update(msg)
		if sm, ok := updated.(components.SpinnerModel); ok {
			m.spinner = sm
		}
		return m, cmd
	}
}

func (m previewSpinnerModel) View() string {
	return m.spinner.View()
}

func waitForPreviewPollDone(done <-chan previewPollResult) tea.Cmd {
	return func() tea.Msg {
		return previewPollDoneMsg(<-done)
	}
}

func pollPreviewWithSpinner(ctx context.Context, client *remote.Client, name string) (*domain.PreviewRoute, error) {
	done := make(chan previewPollResult, 1)
	go func() {
		result, err := pollPreviewStatus(ctx, client, name)
		done <- previewPollResult{preview: result, err: err}
	}()

	if !isInteractiveTerminal() {
		fmt.Printf("Waiting for preview %s...\n", name)
		r := <-done
		return r.preview, r.err
	}

	model := newPreviewSpinnerModel(name, done)
	final, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	fmt.Print("\r\033[K")
	if err != nil {
		return nil, err
	}

	m, ok := final.(previewSpinnerModel)
	if !ok || !m.finished {
		return nil, fmt.Errorf("preview spinner exited unexpectedly")
	}
	return m.outcome.preview, m.outcome.err
}

func pollPreviewStatus(ctx context.Context, client *remote.Client, name string) (*domain.PreviewRoute, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(3 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return &domain.PreviewRoute{Status: "timeout"}, nil
		case <-ticker.C:
			p, err := client.GetPreview(ctx, name)
			if err != nil {
				continue
			}
			if p.Status == domain.PreviewStatusRunning || p.Status == domain.PreviewStatusFailed {
				return p, nil
			}
		}
	}
}

func detectGitBranch(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output() // #nosec G204
	if err != nil {
		return "", fmt.Errorf("detect git branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
