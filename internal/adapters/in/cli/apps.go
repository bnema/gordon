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
	"github.com/bnema/gordon/internal/domain"
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

// resolveAppPlane returns the daemon-backed control plane for app commands.
// App operations are daemon-owned for BOTH local and remote paths
// (05-api-cli.md §1): the explicit remote when one is selected, otherwise
// the owner-only local admin socket. When neither is reachable the command
// fails with daemon-unavailable — there is no local-write fallback. The
// returned handle owns the client and must be closed by the caller.
func resolveAppPlane() (*controlPlaneHandle, error) {
	client, err := resolveAppClient()
	if err != nil {
		return nil, err
	}
	return &controlPlaneHandle{plane: NewRemoteControlPlane(client)}, nil
}

// appMutationError translates ambiguous transport outcomes into the
// outcome-unknown guidance: never blindly retry, re-query by key first.
func appMutationError(op, app, key string, err error) error {
	var unknown *remote.OutcomeUnknownError
	if errors.As(err, &unknown) {
		return fmt.Errorf(
			"outcome-unknown: %s %s may have executed; run the by-key lookup "+
				"gordon apps operations show %s --key %s before retrying with the same key: %w",
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
	return appOpConflictMessage(op, app, key, conflict), true
}

// appOpConflictMessage is the by-key recovery guidance shared by the human
// and JSON renderings of a journaled 409 conflict.
func appOpConflictMessage(op, app, key string, conflict *remote.AppOpConflictError) error {
	return fmt.Errorf(
		"%s of %s did not succeed (outcome %s, op %s); journal rendered above; run the by-key lookup "+
			"gordon apps operations show %s --key %s before retrying with the same key: %w",
		op, app, conflict.Response.Outcome, conflict.Response.Op, app, key, conflict)
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
		newAppsOperationsCmd(),
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppsApply(cmd.Context(), plane, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), file, dryRun, chainDeploy, jsonOut)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to the app manifest file (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate without persisting")
	cmd.Flags().BoolVar(&chainDeploy, "deploy", false, "Deploy the accepted revision after applying")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsApply(ctx context.Context, plane ControlPlane, _ io.Reader, out, errOut io.Writer, file string, dryRun, chainDeploy, jsonOut bool) error {
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
	if jsonOut {
		return runAppsApplyDeployJSON(ctx, plane, resp, out, errOut)
	}
	if err := renderAppApply(out, resp, dryRun); err != nil {
		return err
	}
	return runAppsApplyDeploy(ctx, plane, resp, out, errOut)
}

// appApplyDeployDocument is the single machine-readable apply+deploy result.
// The apply is always included so a failed chained deploy never hides that
// the persist itself succeeded.
type appApplyDeployDocument struct {
	Apply  *dto.AppApplyResponse  `json:"apply"`
	Deploy *dto.AppDeployResponse `json:"deploy"`
}

// runAppsApplyDeploy follows a chained deploy in human mode. The apply outcome
// is already rendered, then an accepted (202 running) deploy reuses the shared
// by-key watch: progress only on change, terminal journal, and Ctrl-C guidance
// that the daemon-side operation keeps running. A terminal partial/failed
// outcome exits nonzero without hiding the successful apply.
func runAppsApplyDeploy(ctx context.Context, plane ControlPlane, apply *dto.AppApplyResponse, out, errOut io.Writer) error {
	deployResp, key, err := plane.DeployApp(ctx, apply.App, dto.AppDeployRequest{Revision: apply.ResultingRevision, All: true})
	if err != nil {
		if conflictErr, ok := renderAppOpConflict(out, "deploy", apply.App, key, err, false); ok {
			return applySucceededError(apply, conflictErr)
		}
		return applySucceededError(apply, appMutationError("deploy", apply.App, key, err))
	}
	if deployResp.Status == dto.AppStatusRunning {
		if werr := watchOperation(ctx, plane, apply.App, key, deployResp, out, errOut, false); werr != nil {
			return applySucceededError(apply, werr)
		}
		return nil
	}
	return renderAppDeployResponse(out, deployResp)
}

// runAppsApplyDeployJSON follows a chained deploy and writes exactly one final
// {apply,deploy} document once the operation is terminal. The mutation is
// issued once and observed through the existing by-key endpoint; no initial
// running document is emitted, and progress, transient warnings, and Ctrl-C
// resume guidance go to errOut so stdout stays machine-readable.
func runAppsApplyDeployJSON(ctx context.Context, plane ControlPlane, apply *dto.AppApplyResponse, out, errOut io.Writer) error {
	deployResp, key, err := plane.DeployApp(ctx, apply.App, dto.AppDeployRequest{Revision: apply.ResultingRevision, All: true})
	if err != nil {
		var conflict *remote.AppOpConflictError
		if errors.As(err, &conflict) {
			if werr := writeJSON(out, appApplyDeployDocument{Apply: apply, Deploy: &conflict.Response}); werr != nil {
				return werr
			}
			return applySucceededError(apply, appOpConflictMessage("deploy", apply.App, key, conflict))
		}
		return applySucceededError(apply, appMutationError("deploy", apply.App, key, err))
	}
	terminal := deployResp
	if deployResp.Status == dto.AppStatusRunning {
		op, werr := awaitOperation(ctx, plane, apply.App, key, deployResp, errOut)
		if werr != nil {
			return applySucceededError(apply, werr)
		}
		terminal = op
	}
	if werr := writeJSON(out, appApplyDeployDocument{Apply: apply, Deploy: terminal}); werr != nil {
		return werr
	}
	if terminal.Outcome == domain.AppOutcomeSuccess {
		return nil
	}
	return applySucceededError(apply, &OperationFailedError{App: apply.App, Op: terminal.Op, Outcome: terminal.Outcome})
}

// applySucceededError preserves the apply outcome in every chained-deploy
// failure path: the desired state was already persisted, so the caller must
// not be told the apply failed.
func applySucceededError(apply *dto.AppApplyResponse, cause error) error {
	return fmt.Errorf("apply of %s succeeded (%s); %w", apply.App, apply.ResultingRevision, cause)
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppsList(cmd.Context(), plane, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsList(ctx context.Context, plane ControlPlane, out io.Writer, jsonOut bool) error {
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
		switch {
		case a.Stopped:
			state = "stopped"
		case a.Pending:
			state = "pending"
		case a.Active == "":
			state = "applied"
		case a.LastOutcome != "" && a.LastOutcome != "success":
			state += " (" + a.LastOutcome + ")"
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppsShow(cmd.Context(), plane, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsShow(ctx context.Context, plane ControlPlane, app string, out io.Writer, jsonOut bool) error {
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
	if resp.LastOp != nil && resp.LastOp.Op != "" {
		if err := cliWriteLine(out, cliRenderMeta("last-op:", resp.LastOp.Op+" ("+resp.LastOp.Outcome+")")); err != nil {
			return err
		}
	}
	owned := append(append([]string(nil), resp.Retained.Volumes...), resp.Retained.Images...)
	owned = append(owned, resp.Retained.Secrets...)
	if len(owned) > 0 {
		sort.Strings(owned)
		return cliWriteLine(out, cliRenderMeta("retained:", strings.Join(owned, " ")))
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppsDiff(cmd.Context(), plane, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsDiff(ctx context.Context, plane ControlPlane, app string, out io.Writer, jsonOut bool) error {
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
--stdin with KEY=VALUE lines, or --stdin --key KEY for one raw value. Names
must already exist in desired or active state. Only key names are ever echoed
back — never values.`,
	}
	cmd.AddCommand(newAppsSecretsListCmd(), newAppsSecretsSetCmd(), newAppsSecretsDeleteCmd())
	return cmd
}

func newAppsSecretsListCmd() *cobra.Command {
	var service string
	var jsonOut bool
	cmd := &cobra.Command{Use: "list APP", Short: "List app secret registration metadata", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		handle, err := resolveAppPlane()
		if err != nil {
			return err
		}
		defer handle.close()
		return runAppsSecretsList(cmd.Context(), handle.plane, cmd.OutOrStdout(), args[0], service, jsonOut)
	}}
	cmd.Flags().StringVar(&service, "service", "", "Filter by service")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// runAppsSecretsList renders metadata-only registrations for one app,
// optionally filtered to one service. Secret values never appear.
func runAppsSecretsList(ctx context.Context, plane ControlPlane, out io.Writer, app, service string, jsonOut bool) error {
	entries, err := plane.ListAppSecrets(ctx, app, service)
	if err != nil {
		return fmt.Errorf("list app secrets for %s: %w", app, err)
	}
	if jsonOut {
		return writeJSON(out, entries)
	}
	for _, entry := range entries {
		if err := cliWriteLine(out, cliRenderMeta(entry.Service+"/"+entry.Key+":", entry.Name+" "+entry.Source+" "+entry.Presence)); err != nil {
			return err
		}
	}
	if len(entries) == 0 {
		return cliWriteLine(out, cliRenderMuted("No registered secrets"))
	}
	return nil
}

// newAppsSecretsSetCmd creates `apps secrets set`.
func newAppsSecretsSetCmd() *cobra.Command {
	var service string
	var key string
	var fromStdin bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "set APP KEY=VALUE…",
		Short: "Write app secret values",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			if len(args) == 0 {
				return fmt.Errorf("missing APP argument")
			}
			return runAppsSecretsSetMode(cmd.Context(), plane, cmd.InOrStdin(), cmd.OutOrStdout(), args[0], args[1:], service, key, fromStdin, jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service the secrets belong to (required)")
	cmd.Flags().StringVar(&key, "key", "", "Secret key for single-value stdin mode")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "Read KEY=VALUE lines, or one raw value with --key")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsSecretsSet(ctx context.Context, plane ControlPlane, stdin io.Reader, out io.Writer, app string, pairs []string, service string, fromStdin, jsonOut bool) error {
	return runAppsSecretsSetMode(ctx, plane, stdin, out, app, pairs, service, "", fromStdin, jsonOut)
}

func runAppsSecretsSetMode(ctx context.Context, plane ControlPlane, stdin io.Reader, out io.Writer, app string, pairs []string, service, key string, fromStdin, jsonOut bool) error {
	if app == "" {
		return fmt.Errorf("missing APP argument")
	}
	if service == "" {
		return fmt.Errorf("missing required flag --service: secrets are service-scoped")
	}
	if key != "" {
		if !fromStdin || len(pairs) != 0 {
			return fmt.Errorf("--key requires --stdin and forbids KEY=VALUE arguments")
		}
		value, err := readRawSecretStdin(stdin)
		if err != nil {
			return err
		}
		pairs = []string{key + "=" + value}
	} else if fromStdin {
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
	if err := cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Set %d secret(s) for %s/%s: %s", len(keys), app, service, strings.Join(keys, ", ")))); err != nil {
		return err
	}
	return cliWriteLine(out, cliRenderMuted(fmt.Sprintf("Running containers keep their old values. Apply them with: gordon apps restart %s --service %s", app, service)))
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppsSecretsDelete(cmd.Context(), plane, cmd.OutOrStdout(), args[0], args[1], service, jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service the secret belongs to (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppsSecretsDelete(ctx context.Context, plane ControlPlane, out io.Writer, app, key, service string, jsonOut bool) error {
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

// parseSecretPairs parses KEY=VALUE arguments. Keys must be non-empty and
// each value must pass validateSecretValue (1-MaxAppEnvValueLen bytes on a
// single line); an empty value is rejected.
func parseSecretPairs(pairs []string) (map[string]string, error) {
	values := make(map[string]string, len(pairs))
	for index, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid secret input %d: expected KEY=VALUE", index+1)
		}
		if err := validateSecretValue(value); err != nil {
			return nil, fmt.Errorf("invalid secret input %d: %w", index+1, err)
		}
		values[key] = value
	}
	return values, nil
}

func validateSecretValue(value string) error {
	if value == "" || len(value) > domain.MaxAppEnvValueLen {
		return fmt.Errorf("value must be 1-%d bytes", domain.MaxAppEnvValueLen)
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("value must be a single line")
	}
	return nil
}

func readRawSecretStdin(in io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(in, domain.MaxAppEnvValueLen+3))
	if err != nil {
		return "", fmt.Errorf("failed to read secret from stdin: %w", err)
	}
	if strings.HasSuffix(string(data), "\r\n") {
		data = data[:len(data)-2]
	} else if strings.HasSuffix(string(data), "\n") {
		data = data[:len(data)-1]
	}
	value := string(data)
	if err := validateSecretValue(value); err != nil {
		return "", fmt.Errorf("invalid stdin secret: %w", err)
	}
	return value, nil
}

const maxSecretStdinBytes = 1 << 20

// readSecretStdin reads bounded KEY=VALUE lines. Whitespace-only lines are
// ignored, while every byte in nonblank values is preserved.
func readSecretStdin(in io.Reader) ([]string, error) {
	data, err := io.ReadAll(io.LimitReader(in, maxSecretStdinBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read secrets from stdin: %w", err)
	}
	if len(data) > maxSecretStdinBytes {
		return nil, fmt.Errorf("secret input exceeds %d bytes", maxSecretStdinBytes)
	}
	var pairs []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) != "" {
			pairs = append(pairs, line)
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
	var all bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "deploy APP",
		Short: "Activate an app revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			req := dto.AppDeployRequest{Revision: revision, Service: service, All: all}
			return runAppDeploy(cmd.Context(), plane, args[0], req, cmd.OutOrStdout(), cmd.ErrOrStderr(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "", "Revision to activate (default: desired head)")
	cmd.Flags().StringVar(&service, "service", "", "Deploy a single service only")
	cmd.Flags().BoolVar(&all, "all", false, "Deploy every service (required for multi-service apps without --service)")
	cmd.MarkFlagsMutuallyExclusive("service", "all")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// runAppDeploy issues one deploy mutation and, when the daemon answers 202
// with a running journal, polls the existing by-key endpoint to terminal
// rather than reissuing the mutation or hiding the outcome.
func runAppDeploy(ctx context.Context, plane ControlPlane, app string, req dto.AppDeployRequest, out, errOut io.Writer, jsonOut bool) error {
	resp, key, err := plane.DeployApp(ctx, app, req)
	if err != nil {
		if conflictErr, ok := renderAppOpConflict(out, "deploy", app, key, err, jsonOut); ok {
			return conflictErr
		}
		return appMutationError("deploy", app, key, err)
	}
	if resp.Status == dto.AppStatusRunning {
		return watchOperation(ctx, plane, app, key, resp, out, errOut, jsonOut)
	}
	if jsonOut {
		return writeJSON(out, resp)
	}
	return renderAppDeployResponse(out, resp)
}

// newAppRestartCmd creates `restart APP`: pinned digests, no re-resolve.
func newAppRestartCmd() *cobra.Command {
	var service string
	var all bool
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "restart APP",
		Short: "Restart an app from pinned digests",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppRestart(cmd.Context(), plane, args[0], service, all, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Restart a single service only")
	cmd.Flags().BoolVar(&all, "all", false, "Restart every service (required for multi-service apps without --service)")
	cmd.MarkFlagsMutuallyExclusive("service", "all")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppRestart(ctx context.Context, plane ControlPlane, app, service string, all bool, out io.Writer, jsonOut bool) error {
	resp, key, err := plane.RestartApp(ctx, app, service, all)
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
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
	if err := renderDeploySteps(out, resp.Steps); err != nil {
		return err
	}
	return renderDeploySummaries(out, resp)
}

func renderDeploySteps(out io.Writer, steps []dto.AppStepDTO) error {
	for _, step := range steps {
		detail := step.State
		if step.Detail != "" {
			detail += ": " + sanitizeTerminalText(step.Detail)
		}
		if step.Error != "" {
			detail += ": " + sanitizeTerminalText(step.Error)
		}
		if err := cliWriteLine(out, cliRenderMeta("  step "+step.ID+":", detail)); err != nil {
			return err
		}
		for _, diagnostic := range step.Diagnostics {
			if err := cliWriteLine(out, cliRenderMuted("    "+sanitizeTerminalText(diagnostic))); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != '\x7f' {
			return r
		}
		return '�'
	}, value)
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
		if svc.Result == domain.AppServiceUnchanged {
			detail = "unchanged (already running this image; use `gordon apps restart` to re-read secrets)"
		}
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
	if resp.Effective != nil && len(resp.Effective.Services) > 0 {
		pairs := make([]string, 0, len(resp.Effective.Services))
		for name, rev := range resp.Effective.Services {
			pairs = append(pairs, name+"="+rev)
		}
		sort.Strings(pairs)
		if err := cliWriteLine(out, cliRenderMeta("effective:", strings.Join(pairs, " "))); err != nil {
			return err
		}
	}
	if resp.Retained == nil {
		return nil
	}
	owned := append(append([]string(nil), resp.Retained.Volumes...), resp.Retained.Images...)
	owned = append(owned, resp.Retained.Secrets...)
	if len(owned) > 0 {
		sort.Strings(owned)
		if err := cliWriteLine(out, cliRenderMeta("retained:", strings.Join(owned, " "))); err != nil {
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
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			plane := handle.plane
			return runAppStatus(cmd.Context(), plane, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppStatus(ctx context.Context, plane ControlPlane, app string, out io.Writer, jsonOut bool) error {
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
			plane := NewRemoteControlPlane(client)
			return runAppLogs(cmd.Context(), plane, &remoteAppLogReader{client: client}, args[0], service, follow, tail, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service to read logs from (required when the app has several)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "Number of lines to show")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAppLogs(ctx context.Context, plane ControlPlane, reader appLogReader, app, service string, follow bool, tail int, out io.Writer, jsonOut bool) error {
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
