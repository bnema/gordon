package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/domain"
)

// newSecretsCmd creates the secrets command group.
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets",
		Long: `Manage secrets (environment variables) for routes and attachments.

Secrets are stored per-domain and injected into containers as environment variables.
Use --attachment to target attachment containers (databases, caches, etc.).

When targeting a remote Gordon instance (via --remote flag or GORDON_REMOTE env var),
these commands operate on the remote server.`,
	}

	cmd.AddCommand(newSecretsListCmd())
	cmd.AddCommand(newSecretsSetCmd())
	cmd.AddCommand(newSecretsRemoveCmd())
	cmd.AddCommand(newSecretsDoctorCmd())
	cmd.AddCommand(newSecretsLockCmd())

	return cmd
}

const managedPassLockReadyLine = "Managed pass backend lock acquired"

var (
	holdManagedPassBackend         = app.HoldManagedPassBackend
	runManagedPassDoctor           = app.RunManagedPassDoctor
	openManagedPassWriteCheckStore = func() (managedPassWriteCheckStore, error) {
		return domainsecrets.NewPassStore(zerowrap.New(cliLogConfig))
	}
)

type managedPassWriteCheckStore interface {
	Set(domainName string, secretsMap map[string]string) error
	GetAll(domainName string) (map[string]string, error)
	Delete(domainName, key string) error
}

func newSecretsLockCmd() *cobra.Command {
	var configFile string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Hold the managed pass backend lease for offline maintenance",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return runSecretsLock(ctx, configFile, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runSecretsLock(ctx context.Context, configFile string, out io.Writer, jsonOut bool) error {
	if err := validateManagedPassConfig(configFile); err != nil {
		return err
	}
	return holdManagedPassBackend(ctx, func() error {
		if jsonOut {
			return writeJSON(out, map[string]string{
				"status":  "locked",
				"message": managedPassLockReadyLine,
			})
		}
		return cliWriteLine(out, managedPassLockReadyLine)
	})
}

func newSecretsDoctorCmd() *cobra.Command {
	var configFile string
	var writeCheck bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Initialize and validate the configured managed pass backend",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSecretsDoctor(cmd.Context(), configFile, writeCheck, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVar(&writeCheck, "write-check", false, "Verify an application-level write/read/delete without displaying values")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runSecretsDoctor(ctx context.Context, configFile string, writeCheck bool, out io.Writer, jsonOut bool) error {
	if err := validateManagedPassConfig(configFile); err != nil {
		return err
	}
	if err := runManagedPassDoctor(ctx, func() error {
		if writeCheck {
			return runManagedPassWriteCheck(ctx)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("validate managed pass backend: %w", err)
	}
	if jsonOut {
		payload := map[string]any{"status": "healthy"}
		if writeCheck {
			payload["write_check"] = true
		}
		return writeJSON(out, payload)
	}
	return cliWriteLine(out, "Managed pass backend is healthy")
}

func validateManagedPassConfig(configFile string) error {
	v := viper.New()
	app.ConfigureViper(v, configFile)
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return fmt.Errorf("read configuration: %w", err)
		}
	}
	backend := resolveLocalSecretsBackend(v)
	if backend != domain.SecretsBackendPass {
		return fmt.Errorf("configured secrets backend is not pass (resolved %q)", backend)
	}
	return nil
}

func runManagedPassWriteCheck(ctx context.Context) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	marker := make([]byte, 16)
	if _, err := rand.Read(marker); err != nil {
		return fmt.Errorf("generate managed pass write check")
	}
	domainName := "doctor-" + fmt.Sprint(time.Now().UnixNano()) + ".invalid"
	const key = "GORDON_DOCTOR_MARKER"
	value := hex.EncodeToString(marker)
	store, err := openManagedPassWriteCheckStore()
	if err != nil {
		return fmt.Errorf("open managed pass backend: %w", err)
	}
	written := false
	defer func() {
		if !written {
			return
		}
		if cleanupErr := store.Delete(domainName, key); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if err := store.Set(domainName, map[string]string{key: value}); err != nil {
		return fmt.Errorf("write managed pass check: %w", err)
	}
	written = true
	if err := ctx.Err(); err != nil {
		return err
	}
	values, getErr := store.GetAll(domainName)
	if getErr != nil || values[key] != value {
		return fmt.Errorf("read managed pass check failed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// newSecretsListCmd creates the secrets list command.
func newSecretsListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list <domain>",
		Short: "List secrets for a domain",
		Long: `List all secret keys configured for a domain.

Note: Only secret keys are shown, not values (for security).
Attachment secrets (for services like databases) are also displayed.

Examples:
  gordon secrets list app.mydomain.com
  gordon --remote https://gordon.mydomain.com secrets list api.mydomain.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsListCmd(cmd, args, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

// runSecretsListCmd executes the secrets list command.
func runSecretsListCmd(cmd *cobra.Command, args []string, jsonOut bool) error {
	ctx := cmd.Context()
	secretDomain := args[0]

	handle, err := resolveControlPlaneForRouteDomain(ctx, secretDomain)
	if err != nil {
		return err
	}
	defer handle.close()

	keys, attachments, err := fetchSecretsWithAttachments(ctx, handle.plane, secretDomain)
	if err != nil {
		return err
	}

	totalSecrets := len(keys)
	for _, att := range attachments {
		totalSecrets += len(att.Keys)
	}

	if totalSecrets == 0 {
		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"domain":      secretDomain,
				"keys":        []string{},
				"attachments": []any{},
			})
		}
		fmt.Println(styles.Theme.Muted.Render(fmt.Sprintf("No secrets configured for %s", secretDomain)))
		return nil
	}

	if jsonOut {
		if attachments == nil {
			attachments = []remote.AttachmentSecrets{}
		}
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"domain":      secretDomain,
			"keys":        keys,
			"attachments": attachments,
		})
	}

	title := fmt.Sprintf("Secrets for %s", secretDomain)
	if !handle.isRemote {
		title = fmt.Sprintf("Secrets for %s (local)", secretDomain)
	}
	fmt.Println(styles.Theme.Title.Render(title))
	fmt.Println()

	rows := buildSecretsTableRows(keys, attachments)

	table := components.NewTable(
		components.WithColumns([]components.TableColumn{
			{Title: "Key", Width: 45},
			{Title: "Value", Width: 10},
		}),
		components.WithRows(rows),
	)

	fmt.Println(table.View())
	return nil
}

// fetchSecretsWithAttachments retrieves secrets from the selected control plane.
func fetchSecretsWithAttachments(ctx context.Context, cp ControlPlane, secretDomain string) ([]string, []remote.AttachmentSecrets, error) {
	result, err := cp.ListSecretsWithAttachments(ctx, secretDomain)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list secrets: %w", err)
	}
	return result.Keys, result.Attachments, nil
}

// buildSecretsTableRows builds table rows with tree structure for attachments.
func buildSecretsTableRows(keys []string, attachments []remote.AttachmentSecrets) [][]string {
	var rows [][]string

	// Domain secrets first
	for _, key := range keys {
		rows = append(rows, []string{key, styles.Theme.Muted.Render("(hidden)")})
	}

	// Attachment secrets with tree structure
	for i, att := range attachments {
		isLastAttachment := i == len(attachments)-1
		rows = append(rows, buildAttachmentRows(att, isLastAttachment)...)
	}

	return rows
}

// buildAttachmentRows builds table rows for a single attachment with tree structure.
func buildAttachmentRows(att remote.AttachmentSecrets, isLastAttachment bool) [][]string {
	var rows [][]string

	// Attachment header with tree prefix
	prefix := styles.IconTreeBranch + styles.IconTreeLine
	if isLastAttachment {
		prefix = styles.IconTreeLast + styles.IconTreeLine
	}

	serviceName := extractServiceName(att.Service)
	attachmentHeader := fmt.Sprintf("%s %s", prefix, styles.Theme.Muted.Render(fmt.Sprintf("[%s]", serviceName)))
	rows = append(rows, []string{attachmentHeader, ""})

	// Keys for this attachment with nested tree structure
	for j, key := range att.Keys {
		isLastKey := j == len(att.Keys)-1
		keyPrefix := getKeyPrefix(isLastAttachment, isLastKey)
		rows = append(rows, []string{keyPrefix + " " + key, styles.Theme.Muted.Render("(hidden)")})
	}

	return rows
}

// extractServiceName extracts a short service name from a container name.
// e.g., "gordon-git-bnema-dev-gitea-postgres" → "gitea-postgres"
func extractServiceName(containerName string) string {
	if !strings.HasPrefix(containerName, "gordon-") {
		return containerName
	}

	parts := strings.SplitN(containerName, "-", 2)
	if len(parts) <= 1 {
		return containerName
	}

	serviceName := parts[1]
	allParts := strings.Split(serviceName, "-")

	// Handle service names based on the number of segments explicitly
	if len(allParts) < 2 {
		// No additional segments; use the service name as-is.
		return serviceName
	}
	if len(allParts) == 2 {
		// Exactly two segments; the service name is already in the desired form.
		return strings.Join(allParts, "-")
	}

	// More than two segments: take the last two as the short service name (e.g., "gitea-postgres").
	return strings.Join(allParts[len(allParts)-2:], "-")
}

// getKeyPrefix returns the tree prefix for a key based on its position.
func getKeyPrefix(isLastAttachment, isLastKey bool) string {
	if isLastAttachment {
		// Parent is last, use space continuation
		if isLastKey {
			return "   " + styles.IconTreeLast + styles.IconTreeLine
		}
		return "   " + styles.IconTreeBranch + styles.IconTreeLine
	}
	// Parent has siblings, use vertical line continuation
	if isLastKey {
		return styles.IconTreeVert + "  " + styles.IconTreeLast + styles.IconTreeLine
	}
	return styles.IconTreeVert + "  " + styles.IconTreeBranch + styles.IconTreeLine
}

// newSecretsSetCmd creates the secrets set command.
func newSecretsSetCmd() *cobra.Command {
	var attachment string

	cmd := &cobra.Command{
		Use:   "set <domain> <KEY=value>...",
		Short: "Set secrets for a domain or attachment",
		Long: `Set one or more secrets for a domain or an attachment container.

Secrets are specified as KEY=value pairs. Multiple secrets can be set at once.

Use --attachment to target an attachment service (e.g., postgres, redis) instead
of the main domain container.

Examples:
  gordon secrets set app.mydomain.com DATABASE_URL=postgres://localhost/db
  gordon secrets set app.mydomain.com API_KEY=secret123 DEBUG=false
  gordon secrets set app.mydomain.com --attachment postgres POSTGRES_PASSWORD=secret
  gordon secrets set app.mydomain.com --attachment redis REDIS_PASSWORD=secret`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretDomain := args[0]
			pairs := args[1:]

			// Parse KEY=value pairs
			secrets := make(map[string]string)
			for _, pair := range pairs {
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid format: %s (expected KEY=value)", pair)
				}
				secrets[parts[0]] = parts[1]
			}

			handle, err := resolveControlPlaneForRouteDomain(ctx, secretDomain)
			if err != nil {
				return err
			}
			defer handle.close()
			if attachment != "" {
				if err := handle.plane.SetAttachmentSecrets(ctx, secretDomain, attachment, secrets); err != nil {
					return fmt.Errorf("failed to set secrets: %w", err)
				}
			} else {
				if err := handle.plane.SetSecrets(ctx, secretDomain, secrets); err != nil {
					return fmt.Errorf("failed to set secrets: %w", err)
				}
			}

			target := secretDomain
			if attachment != "" {
				target = fmt.Sprintf("%s [%s]", secretDomain, attachment)
			}

			if len(secrets) == 1 {
				for key := range secrets {
					fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret set: %s on %s", key, target)))
				}
			} else {
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Set %d secrets for %s", len(secrets), target)))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&attachment, "attachment", "a", "", "Target an attachment service (e.g., postgres, redis)")

	return cmd
}

// newSecretsRemoveCmd creates the secrets remove command.
func newSecretsRemoveCmd() *cobra.Command {
	var (
		force      bool
		attachment string
	)

	cmd := &cobra.Command{
		Use:   "remove <domain> <key>",
		Short: "Remove a secret",
		Long: `Remove a secret from a domain or an attachment container.

Use --attachment to target an attachment service (e.g., postgres, redis) instead
of the main domain container.

Examples:
  gordon secrets remove app.mydomain.com OLD_API_KEY
  gordon secrets remove app.mydomain.com OLD_API_KEY --force
  gordon secrets remove app.mydomain.com --attachment postgres POSTGRES_PASSWORD`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretDomain := args[0]
			key := args[1]

			target := secretDomain
			if attachment != "" {
				target = fmt.Sprintf("%s [%s]", secretDomain, attachment)
			}

			// Confirm unless --force
			if !force {
				confirmed, err := components.RunConfirm(
					fmt.Sprintf("Remove secret '%s' from %s?", key, target),
				)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println(styles.Theme.Muted.Render("Cancelled"))
					return nil
				}
			}

			handle, err := resolveControlPlaneForRouteDomain(ctx, secretDomain)
			if err != nil {
				return err
			}
			defer handle.close()
			if attachment != "" {
				if err := handle.plane.DeleteAttachmentSecret(ctx, secretDomain, attachment, key); err != nil {
					return fmt.Errorf("failed to remove secret: %w", err)
				}
			} else {
				if err := handle.plane.DeleteSecret(ctx, secretDomain, key); err != nil {
					return fmt.Errorf("failed to remove secret: %w", err)
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret removed from %s: %s", target, key)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	cmd.Flags().StringVarP(&attachment, "attachment", "a", "", "Target an attachment service (e.g., postgres, redis)")

	return cmd
}
