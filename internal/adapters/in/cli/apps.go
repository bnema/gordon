package cli

// App CLI surface for declarative app operations.
//
// Conventions (per AGENTS.md): cmd.Context(), cmd.OutOrStdout(), errors
// from RunE, --json via writeJSON with equivalent semantics, stable
// sorted ordering, and NO secret values or sensitive output anywhere.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// appApplyMaxBytes bounds the apply manifest body (05-api-cli.md §2).
const appApplyMaxBytes = 1 << 20

// newLocalAppClient constructs the owner-only local daemon client. It is a
// variable so command tests can inject a socket-backed or unavailable client.
var newLocalAppClient = remote.NewLocalClient

// resolveAppClient returns the daemon client for app operations. An explicit
// remote (flag or GORDON_REMOTE) is authoritative and never falls back to the
// local socket; otherwise the owner-only local admin socket is used.
func resolveAppClient() (*remote.Client, error) {
	client, isRemote, err := GetRemoteClient()
	if err != nil {
		return nil, err
	}
	if isRemote {
		return client, nil
	}

	localClient, err := newLocalAppClient()
	if err != nil {
		return nil, fmt.Errorf(
			"daemon-unavailable: no daemon endpoint is reachable; app operations "+
				"are daemon-owned and have no local-write fallback "+
				"(start the daemon or pass --remote): %w", err)
	}
	return localClient, nil
}

// resolveAppControlPlane returns the daemon-backed app plane. App mutations
// are daemon-owned for BOTH local and remote paths (05-api-cli.md §1): the
// explicit remote when one is selected, otherwise the owner-only local admin
// socket. When neither is reachable the command fails with daemon-unavailable
// — there is no local-write fallback.
func resolveAppControlPlane() (AppControlPlane, error) {
	client, err := resolveAppClient()
	if err != nil {
		return nil, err
	}
	return NewRemoteAppControlPlane(client), nil
}

// appMutationError translates ambiguous transport outcomes into the
// outcome-unknown guidance: never blindly retry, re-query by key first.
func appMutationError(op, app, key string, err error) error {
	var unknown *remote.OutcomeUnknownError
	if errors.As(err, &unknown) {
		return fmt.Errorf(
			"outcome-unknown: %s %s may have executed; query "+
				"GET /admin/apps/%s/operations/by-key/%s before retrying (same key): %w",
			op, app, app, key, err)
	}
	return err
}

// renderAppOpConflict renders the journaled 409 deploy/lifecycle response
// carried by remote.AppOpConflictError, then returns the terminal failure
// (nonzero exit) with the operation key and by-key recovery guidance. It
// reports false when err is not a journaled conflict so callers fall
// through to appMutationError.
func renderAppOpConflict(out io.Writer, op, app, key string, err error, jsonOut bool) (error, bool) {
	var conflict *remote.AppOpConflictError
	if !errors.As(err, &conflict) {
		return nil, false
	}
	if jsonOut {
		if werr := writeJSON(out, conflict.Response); werr != nil {
			return werr, true
		}
	} else if rerr := renderAppDeployResponse(out, &conflict.Response); rerr != nil {
		return rerr, true
	}
	return fmt.Errorf(
		"%s of %s did not succeed (outcome %s, op %s); journal rendered above; query "+
			"GET /admin/apps/%s/operations/by-key/%s before retrying (same key): %w",
		op, app, conflict.Response.Outcome, conflict.Response.Op, app, key, err), true
}

// newAppsCmd creates the `apps` parent command.
func newAppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Manage applications (daemon-owned)",
		Long: `Validate, persist, inspect, and operate applications.

All mutations are executed by the daemon through the admin API;
without a reachable daemon the commands fail instead of writing locally.

Examples:
  gordon apps apply --file blog.toml
  gordon apps list
  gordon apps show blog
  gordon apps diff blog`,
	}
	cmd.AddCommand(
		newAppsApplyCmd(),
		newAppsListCmd(),
		newAppsShowCmd(),
		newAppsDiffCmd(),
		newAppsSecretsCmd(),
		newAppDeployCmd(),
		newAppRestartCmd(),
		newAppStopCmd(),
		newAppStartCmd(),
		newAppRemoveCmd(),
		newAppStatusCmd(),
		newAppLogsCmd(),
	)
	return cmd
}

// newAppsApplyCmd creates `apps apply`.
func newAppsApplyCmd() *cobra.Command {
	var file string
	var dryRun bool
	var chainDeploy bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Validate and persist an app manifest",
		Long: `Validates a manifest file and persists it as desired state (or dry-runs).

With --deploy, chains exactly the accepted revision into a deploy after
persistence succeeds; the two outcomes are reported separately because a
deploy may fail after the apply succeeded.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppsApply(cmd.Context(), plane, os.Stdin, cmd.OutOrStdout(), file, dryRun, chainDeploy, jsonOut)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to the app manifest file (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate without persisting")
	cmd.Flags().BoolVar(&chainDeploy, "deploy", false, "Deploy the accepted revision after applying")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsApply(ctx context.Context, plane AppControlPlane, _ io.Reader, out io.Writer, file string, dryRun, chainDeploy, jsonOut bool) error {
	if dryRun && chainDeploy {
		return fmt.Errorf("cannot combine --dry-run with --deploy: dry-run persists nothing to deploy")
	}
	if file == "" {
		return fmt.Errorf("missing required flag --file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("failed to read manifest file: %w", err)
	}
	if len(data) > appApplyMaxBytes {
		return fmt.Errorf("manifest exceeds the 1 MiB apply bound (%d bytes)", len(data))
	}
	resp, err := plane.ApplyApp(ctx, dto.AppApplyRequest{ManifestTOML: string(data), DryRun: dryRun})
	if err != nil {
		return err
	}
	if !chainDeploy {
		if jsonOut {
			return writeJSON(out, resp)
		}
		return renderAppApply(out, resp, dryRun)
	}
	if !jsonOut {
		if err := renderAppApply(out, resp, dryRun); err != nil {
			return err
		}
	}
	deployResp, key, deployErr := plane.DeployApp(ctx, resp.App, dto.AppDeployRequest{Revision: resp.ResultingRevision})
	if deployErr != nil {
		if conflictErr, ok := renderAppOpConflict(out, "deploy", resp.App, key, deployErr, jsonOut); ok {
			return fmt.Errorf("apply of %s succeeded (%s); %w",
				resp.App, resp.ResultingRevision, conflictErr)
		}
		return fmt.Errorf("apply of %s succeeded (%s); %w",
			resp.App, resp.ResultingRevision, appMutationError("deploy", resp.App, key, deployErr))
	}
	if jsonOut {
		return writeJSON(out, struct {
			Apply  *dto.AppApplyResponse  `json:"apply"`
			Deploy *dto.AppDeployResponse `json:"deploy"`
		}{Apply: resp, Deploy: deployResp})
	}
	return renderAppDeployResponse(out, deployResp)
}

func renderAppApply(out io.Writer, resp *dto.AppApplyResponse, dryRun bool) error {
	if resp.Noop {
		return cliWriteLine(out, cliRenderMuted(fmt.Sprintf("No changes for %s%s", resp.App, appRevisionSuffix(resp.ResultingRevision))))
	}
	verb := "Applied"
	if dryRun {
		verb = "Validated (dry-run)"
	}
	if err := cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("%s %s%s", verb, resp.App, appRevisionSuffix(resp.ResultingRevision)))); err != nil {
		return err
	}
	if err := renderAppDiffSection(out, resp.Diff); err != nil {
		return err
	}
	if resp.Pending {
		return cliWriteLine(out, cliRenderMeta("intent:", resp.Intent))
	}
	return nil
}

// appRevisionSuffix omits the parenthesized revision when empty (dry-run
// validation may not produce a revision).
func appRevisionSuffix(revision string) string {
	if revision == "" {
		return ""
	}
	return " (" + revision + ")"
}

// renderAppDiffSection renders normalized diff paths using the same
// added/removed/changed sections as `apps diff`; empty diffs render nothing.
func renderAppDiffSection(out io.Writer, diff dto.AppDiffSection) error {
	if len(diff.Added) == 0 && len(diff.Removed) == 0 && len(diff.Changed) == 0 {
		return nil
	}
	for _, section := range []struct {
		title string
		paths []string
	}{
		{"added:", diff.Added},
		{"removed:", diff.Removed},
		{"changed:", diff.Changed},
	} {
		paths := append([]string(nil), section.paths...)
		sort.Strings(paths)
		for _, p := range paths {
			if err := cliWriteLine(out, cliRenderMeta(section.title, p)); err != nil {
				return err
			}
		}
	}
	return nil
}

// newAppsListCmd creates `apps list`.
func newAppsListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List applications",
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppsList(cmd.Context(), plane, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsList(ctx context.Context, plane AppControlPlane, out io.Writer, jsonOut bool) error {
	apps, err := plane.ListApps(ctx)
	if err != nil {
		return err
	}
	if jsonOut {
		if apps == nil {
			apps = []dto.AppSummaryDTO{}
		}
		return writeJSON(out, apps)
	}
	if len(apps) == 0 {
		return cliWriteLine(out, cliRenderEmptyState("No apps."))
	}
	names := make([]string, 0, len(apps))
	byName := make(map[string]dto.AppSummaryDTO, len(apps))
	for _, a := range apps {
		names = append(names, a.App)
		byName[a.App] = a
	}
	sort.Strings(names)
	for _, name := range names {
		a := byName[name]
		state := "active"
		if a.Stopped {
			state = "stopped"
		} else if !a.Converged {
			state = "pending"
		}
		if err := cliWriteLine(out, cliRenderListItem(fmt.Sprintf("%s (%s)", name, state))); err != nil {
			return err
		}
	}
	return nil
}

// newAppsShowCmd creates `apps show`.
func newAppsShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show APP",
		Short: "Show desired and active state for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppsShow(cmd.Context(), plane, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsShow(ctx context.Context, plane AppControlPlane, app string, out io.Writer, jsonOut bool) error {
	resp, err := plane.ShowApp(ctx, app)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	if err := cliWriteLine(out, cliRenderTitle(resp.App)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("desired:", resp.Desired.Revision+" ("+resp.Desired.Status+")")); err != nil {
		return err
	}
	for _, line := range renderActiveServices(resp.Active) {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	if resp.Intent.Stopped {
		if err := cliWriteLine(out, cliRenderWarning("stopped intent is set")); err != nil {
			return err
		}
	}
	if resp.LastOp.Op != "" {
		return cliWriteLine(out, cliRenderMeta("last-op:", resp.LastOp.Op+" ("+resp.LastOp.Outcome+")"))
	}
	return nil
}

// renderActiveServices renders per-service effective state, sorted by name.
// Only ids and digests appear — never secret values.
func renderActiveServices(active dto.AppActiveDTO) []string {
	names := make([]string, 0, len(active.Services))
	for name := range active.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names)+1)
	converged := "converged"
	if !active.Converged {
		converged = "diverged"
	}
	lines = append(lines, cliRenderMeta("active:", converged))
	for _, name := range names {
		svc := active.Services[name]
		detail := svc.EffectiveRevision
		if svc.Container != "" {
			detail += " container=" + svc.Container
		}
		if svc.RestartUnsafe {
			detail += " restart_unsafe"
		}
		lines = append(lines, cliRenderMeta("  "+name+":", detail))
	}
	return lines
}

// newAppsDiffCmd creates `apps diff`.
func newAppsDiffCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "diff APP",
		Short: "Show the normalized desired-vs-active diff",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppsDiff(cmd.Context(), plane, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsDiff(ctx context.Context, plane AppControlPlane, app string, out io.Writer, jsonOut bool) error {
	resp, err := plane.DiffApp(ctx, app)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	if len(resp.Diff.Added) == 0 && len(resp.Diff.Removed) == 0 && len(resp.Diff.Changed) == 0 {
		return cliWriteLine(out, cliRenderMuted(fmt.Sprintf("No differences for %s.", app)))
	}
	for _, section := range []struct {
		title string
		paths []string
	}{
		{"added:", resp.Diff.Added},
		{"removed:", resp.Diff.Removed},
		{"changed:", resp.Diff.Changed},
	} {
		paths := append([]string(nil), section.paths...)
		sort.Strings(paths)
		for _, p := range paths {
			if err := cliWriteLine(out, cliRenderMeta(section.title, p)); err != nil {
				return err
			}
		}
	}
	return nil
}

// newAppsSecretsCmd creates the `apps secrets` parent command.
func newAppsSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage app secret values",
		Long: `Values are accepted via KEY=VALUE arguments (discouraged: shell history),
--stdin (preferred), or an interactive prompt. Names must already exist in
desired or active state. Only key names are ever echoed back — never values.`,
	}
	cmd.AddCommand(newAppsSecretsSetCmd(), newAppsSecretsDeleteCmd())
	return cmd
}

// newAppsSecretsSetCmd creates `apps secrets set`.
func newAppsSecretsSetCmd() *cobra.Command {
	var service string
	var fromStdin bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "set APP KEY=VALUE…",
		Short: "Write app secret values",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return fmt.Errorf("missing APP argument")
			}
			return runAppsSecretsSet(cmd.Context(), plane, os.Stdin, cmd.OutOrStdout(), args[0], args[1:], service, fromStdin, jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service the secrets belong to (required)")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "Read KEY=VALUE lines from stdin")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsSecretsSet(ctx context.Context, plane AppControlPlane, stdin io.Reader, out io.Writer, app string, pairs []string, service string, fromStdin, jsonOut bool) error {
	if app == "" {
		return fmt.Errorf("missing APP argument")
	}
	if service == "" {
		return fmt.Errorf("missing required flag --service: secrets are service-scoped")
	}
	if fromStdin {
		stdinPairs, err := readSecretStdin(stdin)
		if err != nil {
			return err
		}
		pairs = append(pairs, stdinPairs...)
	}
	values, err := parseSecretPairs(pairs)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return fmt.Errorf("no secrets provided: pass KEY=VALUE arguments or --stdin")
	}
	if err := plane.SetAppSecrets(ctx, app, dto.AppSecretSetRequest{Service: service, Secrets: values}); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if jsonOut {
		return writeJSON(out, map[string]any{"app": app, "service": service, "keys": keys})
	}
	return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Set %d secret(s) for %s/%s: %s", len(keys), app, service, strings.Join(keys, ", "))))
}

// newAppsSecretsDeleteCmd creates `apps secrets delete`.
func newAppsSecretsDeleteCmd() *cobra.Command {
	var service string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "delete APP KEY",
		Short: "Delete an app secret value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppsSecretsDelete(cmd.Context(), plane, cmd.OutOrStdout(), args[0], args[1], service, jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service the secret belongs to (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsSecretsDelete(ctx context.Context, plane AppControlPlane, out io.Writer, app, key, service string, jsonOut bool) error {
	if service == "" {
		return fmt.Errorf("missing required flag --service: secrets are service-scoped")
	}
	if key == "" {
		return fmt.Errorf("missing KEY argument")
	}
	if err := plane.DeleteAppSecret(ctx, app, dto.AppSecretDeleteRequest{Service: service, Key: key}); err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, map[string]any{"app": app, "service": service, "deleted": key})
	}
	return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Deleted secret %s for %s/%s", key, app, service)))
}

// parseSecretPairs parses KEY=VALUE arguments. Keys must be non-empty;
// values may be empty (explicit empty secret).
func parseSecretPairs(pairs []string) (map[string]string, error) {
	values := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid secret %q: expected KEY=VALUE", pair)
		}
		values[key] = value
	}
	return values, nil
}

// readSecretStdin reads KEY=VALUE lines (blank lines ignored).
func readSecretStdin(in io.Reader) ([]string, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, fmt.Errorf("failed to read secrets from stdin: %w", err)
	}
	var pairs []string
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			pairs = append(pairs, trimmed)
		}
	}
	return pairs, nil
}

// newAppDeployCmd creates `deploy APP`. At cutover this replaces the
// domain-based deploy for apps; domain-based deploy keeps working for
// non-app resources until then (05-api-cli.md §5).
func newAppDeployCmd() *cobra.Command {
	var revision string
	var service string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "deploy APP",
		Short: "Activate an app revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppDeploy(cmd.Context(), plane, args[0], revision, service, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "", "Revision to activate (default: desired head)")
	cmd.Flags().StringVar(&service, "service", "", "Deploy a single service only")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppDeploy(ctx context.Context, plane AppControlPlane, app, revision, service string, out io.Writer, jsonOut bool) error {
	resp, key, err := plane.DeployApp(ctx, app, dto.AppDeployRequest{Revision: revision, Service: service})
	if err != nil {
		if conflictErr, ok := renderAppOpConflict(out, "deploy", app, key, err, jsonOut); ok {
			return conflictErr
		}
		return appMutationError("deploy", app, key, err)
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	return renderAppDeployResponse(out, resp)
}

// newAppRestartCmd creates `restart APP`: pinned digests, no re-resolve.
func newAppRestartCmd() *cobra.Command {
	var service string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "restart APP",
		Short: "Restart an app from pinned digests",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppRestart(cmd.Context(), plane, args[0], service, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Restart a single service only")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppRestart(ctx context.Context, plane AppControlPlane, app, service string, out io.Writer, jsonOut bool) error {
	resp, key, err := plane.RestartApp(ctx, app, service)
	if err != nil {
		if conflictErr, ok := renderAppOpConflict(out, "restart", app, key, err, jsonOut); ok {
			return conflictErr
		}
		return appMutationError("restart", app, key, err)
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	return renderAppDeployResponse(out, resp)
}

// newAppStopCmd creates `stop APP`: durable stopped intent, data preserved.
func newAppStopCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stop APP",
		Short: "Stop an app (preserves all data)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppLifecycle(cmd.Context(), plane.StopApp, "stop", args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// newAppStartCmd creates `start APP`: clears intent, ensures running.
func newAppStartCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "start APP",
		Short: "Start a stopped app from active state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppLifecycle(cmd.Context(), plane.StartApp, "start", args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// newAppRemoveCmd creates `remove APP`: withdraws workloads, retains data.
func newAppRemoveCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "remove APP",
		Short: "Remove app workloads (volumes and secrets are retained)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppLifecycle(cmd.Context(), plane.RemoveApp, "remove", args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// appLifecycleFunc is one body-less app lifecycle mutation.
type appLifecycleFunc func(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)

func runAppLifecycle(ctx context.Context, fn appLifecycleFunc, op, app string, out io.Writer, jsonOut bool) error {
	resp, key, err := fn(ctx, app)
	if err != nil {
		if conflictErr, ok := renderAppOpConflict(out, op, app, key, err, jsonOut); ok {
			return conflictErr
		}
		return appMutationError(op, app, key, err)
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	return renderAppDeployResponse(out, resp)
}

// renderAppDeployResponse renders the deploy outcome with terminal
// per-service results, cleanup warnings, and the effective/retained
// summaries. Sorts services for stable output.
func renderAppDeployResponse(out io.Writer, resp *dto.AppDeployResponse) error {
	if err := renderDeployHeadline(out, resp); err != nil {
		return err
	}
	if err := renderDeployServices(out, resp); err != nil {
		return err
	}
	return renderDeploySummaries(out, resp)
}

func renderDeployHeadline(out io.Writer, resp *dto.AppDeployResponse) error {
	headline := fmt.Sprintf("%s %s: %s", resp.Op, resp.App, resp.Outcome)
	if resp.Outcome == "success" {
		return cliWriteLine(out, cliRenderSuccess(headline))
	}
	return cliWriteLine(out, cliRenderWarning(headline))
}

func renderDeployServices(out io.Writer, resp *dto.AppDeployResponse) error {
	names := make([]string, 0, len(resp.Services))
	for name := range resp.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := resp.Services[name]
		detail := svc.Result + " " + svc.EffectiveRevision
		if svc.RestartUnsafe {
			detail += " restart_unsafe"
		}
		if svc.Error != "" {
			detail += ": " + svc.Error
		}
		if err := cliWriteLine(out, cliRenderMeta("  "+name+":", detail)); err != nil {
			return err
		}
	}
	for _, w := range resp.CleanupWarnings {
		if err := cliWriteLine(out, cliRenderWarning(fmt.Sprintf("cleanup: %s left %s (%s)", w.Service, w.Leftover, w.Detail))); err != nil {
			return err
		}
	}
	return nil
}

func renderDeploySummaries(out io.Writer, resp *dto.AppDeployResponse) error {
	if len(resp.Effective.Services) > 0 {
		pairs := make([]string, 0, len(resp.Effective.Services))
		for name, rev := range resp.Effective.Services {
			pairs = append(pairs, name+"="+rev)
		}
		sort.Strings(pairs)
		if err := cliWriteLine(out, cliRenderMeta("effective:", strings.Join(pairs, " "))); err != nil {
			return err
		}
	}
	if len(resp.Retained.Volumes) > 0 || len(resp.Retained.Secrets) > 0 {
		retained := append(append([]string(nil), resp.Retained.Volumes...), resp.Retained.Secrets...)
		sort.Strings(retained)
		if err := cliWriteLine(out, cliRenderMeta("retained:", strings.Join(retained, " "))); err != nil {
			return err
		}
	}
	return nil
}

// newAppStatusCmd creates `status APP`: effective vs observed, per service.
func newAppStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status APP",
		Short: "Show effective vs observed state for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plane, err := resolveAppControlPlane()
			if err != nil {
				return err
			}
			return runAppStatus(cmd.Context(), plane, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppStatus(ctx context.Context, plane AppControlPlane, app string, out io.Writer, jsonOut bool) error {
	resp, err := plane.ShowApp(ctx, app)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	if err := cliWriteLine(out, cliRenderTitle(resp.App)); err != nil {
		return err
	}
	for _, line := range renderActiveServices(resp.Active) {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	var running []string
	for _, svc := range resp.Active.Services {
		if svc.Container != "" {
			running = append(running, svc.Container)
		}
	}
	sort.Strings(running)
	observed := "none"
	if len(running) > 0 {
		observed = strings.Join(running, " ")
	}
	if err := cliWriteLine(out, cliRenderMeta("observed:", observed)); err != nil {
		return err
	}
	if resp.Intent.Stopped {
		return cliWriteLine(out, cliRenderWarning("stopped intent is set"))
	}
	return nil
}

// appLogReader streams logs by Gordon app/service reference. The daemon
// resolves the reference through its authoritative ACTIVE record.
type appLogReader interface {
	GetContainerLogs(ctx context.Context, ref string, lines int) ([]string, error)
	StreamContainerLogs(ctx context.Context, ref string, lines int) (<-chan string, error)
}

// remoteAppLogReader adapts *remote.Client to appLogReader.
type remoteAppLogReader struct {
	client *remote.Client
}

func (r *remoteAppLogReader) GetContainerLogs(ctx context.Context, ref string, lines int) ([]string, error) {
	return r.client.GetContainerLogs(ctx, ref, lines)
}

func (r *remoteAppLogReader) StreamContainerLogs(ctx context.Context, ref string, lines int) (<-chan string, error) {
	return r.client.StreamContainerLogs(ctx, ref, lines)
}

// newAppLogsCmd creates `logs APP`.
func newAppLogsCmd() *cobra.Command {
	var service string
	var follow bool
	var tail int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "logs APP",
		Short: "Show logs for an app service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := resolveAppClient()
			if err != nil {
				return err
			}
			plane := NewRemoteAppControlPlane(client)
			return runAppLogs(cmd.Context(), plane, &remoteAppLogReader{client: client}, args[0], service, follow, tail, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service to read logs from (required when the app has several)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "Number of lines to show")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppLogs(ctx context.Context, plane AppControlPlane, reader appLogReader, app, service string, follow bool, tail int, out io.Writer, jsonOut bool) error {
	if follow && jsonOut {
		return fmt.Errorf("cannot combine --json with --follow: follow streams plain log lines")
	}
	show, err := plane.ShowApp(ctx, app)
	if err != nil {
		return err
	}
	ref, err := appLogRef(show, service)
	if err != nil {
		return err
	}
	if follow {
		stream, err := reader.StreamContainerLogs(ctx, ref, tail)
		if err != nil {
			return err
		}
		for line := range stream {
			if err := cliWriteLine(out, line); err != nil {
				return err
			}
		}
		return nil
	}
	lines, err := reader.GetContainerLogs(ctx, ref, tail)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, map[string]any{"app": app, "service": service, "lines": lines})
	}
	for _, line := range lines {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	return nil
}

// appLogRef resolves the container ref for one app service. When service
// is empty and the app has exactly one service, that service is used;
// otherwise --service is required. A service with no recorded container
// has nothing to stream yet.
func appLogRef(show *dto.AppShowResponse, service string) (string, error) {
	if len(show.Active.Services) == 0 {
		return "", fmt.Errorf("app %s has no active services yet", show.App)
	}
	if service == "" {
		if len(show.Active.Services) == 1 {
			for name := range show.Active.Services {
				service = name
			}
		} else {
			names := make([]string, 0, len(show.Active.Services))
			for name := range show.Active.Services {
				names = append(names, name)
			}
			sort.Strings(names)
			return "", fmt.Errorf("app %s has several services (%s): pass --service", show.App, strings.Join(names, ", "))
		}
	}
	svc, ok := show.Active.Services[service]
	if !ok {
		return "", fmt.Errorf("app %s has no service %q", show.App, service)
	}
	if svc.Container == "" {
		return "", fmt.Errorf("service %s has no recorded container yet", service)
	}
	return show.App + "/" + service, nil
}
