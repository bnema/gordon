package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/pkg/duration"
)

// newAuthCmd creates the auth command group.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Gordon server authentication",
		Long:  `Commands for managing Gordon server authentication tokens and passwords.`,
	}

	cmd.AddCommand(newAuthTokenCmd())
	cmd.AddCommand(newAuthInternalCmd())
	cmd.AddCommand(newAuthLoginCmd())
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthShowTokenCmd())
	cmd.AddCommand(newAuthLogoutCmd())

	return cmd
}

// newAuthTokenCmd creates the token subcommand group.
func newAuthTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage authentication tokens",
		Long:  `Commands for generating, listing, and revoking authentication tokens.`,
	}

	cmd.AddCommand(newTokenGenerateCmd())
	cmd.AddCommand(newTokenListCmd())
	cmd.AddCommand(newTokenRevokeCmd())

	return cmd
}

// newTokenGenerateCmd creates the token generate command.
func newTokenGenerateCmd() *cobra.Command {
	var (
		subject    string
		scopes     string
		expiry     string
		configPath string
		repo       string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a new authentication token",
		Long: `Generate a new JWT authentication token for registry and/or admin access.

The token will be stored in the configured secrets backend and can be used
for docker login as: docker login -u <subject> -p <token>

SCOPES:

Registry scopes (for Docker push/pull):
  push              Push images to registry
  pull              Pull images from registry

Admin scopes (for remote CLI access):
  Format: admin:<resource>:<actions>

  Resources: routes, secrets, config, status, logs, volumes, * (all)
  Actions:   read, write, * (all)

  Examples:
    admin:*:*              Full admin access (recommended for CLI)
    admin:routes:read      Read-only routes access
    admin:status:read      Read-only status access
    admin:logs:read        Read-only log access
    admin:volumes:read     Read-only volume access
    admin:volumes:write    Volume management access

Repository scoping:
  --repo myapp           Scope to specific repository
  --repo "*"             All repositories (default)

Combined examples:
  --scopes "push,pull"                          All repos, registry only (default)
  --scopes "push" --repo myapp                  Push to myapp only
  --scopes "push,pull,admin:routes:read"        Minimum CI scope (all repos)
  --scopes "push,admin:routes:read" --repo app  Minimum CI scope (specific repo)

EXPIRY:

Supports human-friendly durations:
  d (days):   1d = 24 hours      w (weeks):  1w = 7 days
  M (months): 1M = 30 days       y (years):  1y = 365 days

Examples: 1y, 30d, 2w, 6M, 1y6M, 2w3d
Use --expiry=0 for a token that never expires (useful for CI).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenGenerate(subject, scopes, expiry, configPath, repo)
		},
	}

	cmd.Flags().StringVar(&subject, "subject", "", "Subject/username for the token (required)")
	cmd.Flags().StringVar(&scopes, "scopes", "push,pull", "Comma-separated list of scopes")
	cmd.Flags().StringVar(&expiry, "expiry", "30d", "Token expiry duration (e.g., 1y, 30d, 2w, 24h, 0 for never)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().StringVar(&repo, "repo", "*", "Repository to scope the token to (default: * for all repositories)")

	_ = cmd.MarkFlagRequired("subject")

	return cmd
}

// newTokenListCmd creates the token list command.
func newTokenListCmd() *cobra.Command {
	var (
		configPath string
		jsonOut    bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all authentication tokens",
		Long:  `List all stored authentication tokens with their subjects and expiry information.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenList(configPath, cmd.OutOrStdout(), jsonOut)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

// newTokenRevokeCmd creates the token revoke command.
func newTokenRevokeCmd() *cobra.Command {
	var (
		configPath string
		all        bool
	)

	cmd := &cobra.Command{
		Use:   "revoke [token-id]",
		Short: "Revoke authentication token(s)",
		Long: `Revoke a token by its ID, or all tokens with --all.

Examples:
  gordon auth token revoke abc123-def456    Revoke specific token
  gordon auth token revoke --all            Revoke all tokens`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				return runTokenRevokeAll(configPath)
			}
			if len(args) == 0 {
				return fmt.Errorf("token ID required (or use --all to revoke all tokens)")
			}
			return runTokenRevoke(args[0], configPath)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVar(&all, "all", false, "Revoke all tokens")

	return cmd
}

// newAuthInternalCmd creates the internal credentials command.
func newAuthInternalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "internal",
		Short: "Show internal registry credentials for manual recovery",
		Long: `Display the auto-generated internal registry credentials.

These credentials are used by Gordon for pulling images from the local registry
(localhost:5000) during internal deploys. You can use them for manual recovery:

  docker login localhost:5000 -u gordon-internal -p <password>

Note: Credentials are regenerated each time Gordon starts, and are only
available while Gordon is running.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShowInternalAuth()
		},
	}
}

// newAuthLoginCmd creates the login command for remote authentication.
func newAuthLoginCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with a Gordon server",
		Long: `Store or verify a token for a Gordon server remote.

With --token: stores the token and verifies it.
Without --token: verifies the existing stored token still works.

Generate a token on the server with: gordon auth token generate

Examples:
	gordon auth login --token <token>              Store token for active remote
	gordon auth login --remote prod --token <tok>  Store token for specific remote
	gordon auth login                              Verify existing token`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if token != "" {
				return runAuthLoginWithToken(cmd.Context(), token, cmd.OutOrStdout())
			}
			return runAuthLoginVerify(cmd.Context(), cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&token, "token", "t", "", "Authentication token to store for the remote")

	return cmd
}

func resolveRequiredRemote(flagToken string) (*remote.ResolvedRemote, error) {
	resolved, isRemote := remote.Resolve(remoteFlag, flagToken, insecureTLSFlag)
	if !isRemote {
		return nil, fmt.Errorf("no remote specified. Use --remote or 'gordon remotes use <name>'")
	}

	return resolved, nil
}

func runAuthLoginWithToken(ctx context.Context, token string, out io.Writer) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}

	// Resolve remote — but use the new token (not the stored one) for verification.
	resolved, err := resolveRequiredRemote("")
	if err != nil {
		return err
	}

	client := remote.NewClient(resolved.URL, remoteClientOptions(token, resolved.InsecureTLS)...)
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	verified := true
	if _, err := client.GetStatus(verifyCtx); err != nil {
		_ = cliWriteLine(out, styles.RenderWarning(fmt.Sprintf("Token verification failed: %v", err)))
		verified = false
	}

	// Store token — requires a named remote.
	if resolved.Name == "" {
		_ = cliWriteLine(out, styles.RenderWarning("Ad-hoc URL: token verified but cannot be stored. Use 'gordon remotes add' to save this remote."))
		return nil
	}
	if err := remote.UpdateRemoteToken(resolved.Name, token); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}

	_ = cliWriteLine(out, "")
	_ = cliWriteLine(out, styles.RenderSuccess(fmt.Sprintf("Token stored for remote '%s'", resolved.Name)))
	if verified {
		_ = cliWriteLine(out, styles.RenderSuccess("Token verified with remote status endpoint"))
	}
	return nil
}

func runAuthLoginVerify(ctx context.Context, out io.Writer) error {
	resolved, err := resolveRequiredRemote(tokenFlag)
	if err != nil {
		return err
	}
	if resolved.Token == "" {
		return fmt.Errorf("no token stored for remote '%s'; use: gordon auth login --token <token>", resolved.DisplayName())
	}

	client := remote.NewClient(resolved.URL, remoteClientOptions(resolved.Token, resolved.InsecureTLS)...)
	verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := client.GetStatus(verifyCtx); err != nil {
		_ = cliWriteLine(out, styles.RenderWarning(fmt.Sprintf("Token verification failed for '%s': %v", resolved.DisplayName(), err)))
		return fmt.Errorf("authentication failed")
	}

	_ = cliWriteLine(out, styles.RenderSuccess(fmt.Sprintf("Authenticated to '%s'", resolved.DisplayName())))
	return nil
}

// newAuthShowTokenCmd creates the show-token command.
func newAuthShowTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show-token",
		Short: "Print the stored token for a remote",
		Long: `Print the raw authentication token for a remote to stdout.

The token is resolved from pass, TOML config, or environment variable
using the same precedence as other commands.

Output is suitable for piping (no decoration, single line).

Examples:
  gordon auth show-token                     Show token for active remote
  gordon auth show-token --remote prod       Show token for specific remote
  gordon auth show-token | pbcopy            Copy token to clipboard`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthShowToken(cmd.OutOrStdout())
		},
	}

	return cmd
}

func runAuthShowToken(out io.Writer) error {
	resolved, err := resolveRequiredRemote(tokenFlag)
	if err != nil {
		return err
	}
	if resolved.Token == "" {
		return fmt.Errorf("no token found for remote '%s'", resolved.DisplayName())
	}

	if f, ok := out.(*os.File); ok && isatty.IsTerminal(f.Fd()) {
		_ = cliWriteLine(os.Stderr, styles.RenderWarning("Warning: token will be visible in terminal scrollback"))
	}

	_ = cliWriteLine(out, resolved.Token)
	return nil
}

// newAuthLogoutCmd creates the logout command.
func newAuthLogoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored authentication for a remote",
		Long: `Remove the stored token for a remote from pass and TOML config.

This clears the token from all local storage but does not revoke the
token on the server. To revoke server-side, use: gordon auth token revoke

Examples:
  gordon auth logout                   Logout from active remote
  gordon auth logout --remote prod     Logout from specific remote`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthLogout(cmd.OutOrStdout())
		},
	}

	return cmd
}

func runAuthLogout(out io.Writer) error {
	resolved, err := resolveRequiredRemote(tokenFlag)
	if err != nil {
		return err
	}
	if resolved.Name == "" {
		return fmt.Errorf("cannot logout from ad-hoc URL. Use 'gordon remotes add' to save this remote first")
	}

	if err := remote.ClearRemoteToken(resolved.Name); err != nil {
		return fmt.Errorf("failed to clear token: %w", err)
	}

	_ = cliWriteLine(out, styles.RenderSuccess(fmt.Sprintf("Logged out from remote '%s'", resolved.Name)))
	return nil
}

// runShowInternalAuth displays the internal registry credentials.
func runShowInternalAuth() error {
	creds, err := app.GetInternalCredentials()
	if err != nil {
		return err
	}

	fmt.Println("Internal Registry Credentials")
	fmt.Println("==============================")
	fmt.Printf("Username: %s\n", creds.Username)
	fmt.Printf("Password: %s\n", creds.Password)
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Printf("  docker login localhost:5000 -u %s -p %s\n", creds.Username, creds.Password)

	return nil
}

// runTokenGenerate generates a new authentication token.
// parseAndConvertScopes parses a comma-separated scope string and converts
// simple scopes (push, pull, *) to Docker v2 format (repository:*:action).
func parseAndConvertScopes(scopesStr, repo string) ([]string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return nil, fmt.Errorf("repository name cannot be empty")
	}

	rawScopes := strings.Split(scopesStr, ",")
	for i := range rawScopes {
		rawScopes[i] = strings.TrimSpace(rawScopes[i])
	}

	// Check if scopes are already in v2 format (contain colons)
	hasSimpleScope := false
	for _, s := range rawScopes {
		if !strings.Contains(s, ":") && (s == "push" || s == "pull" || s == "*") {
			hasSimpleScope = true
			break
		}
	}

	if !hasSimpleScope {
		return rawScopes, nil
	}

	// Convert simple scopes to v2 format: repository:<repo>:push,pull
	var scopes []string
	var actions []string
	for _, s := range rawScopes {
		if s == "push" || s == "pull" || s == "*" {
			actions = append(actions, s)
		} else {
			// Keep non-simple scopes as-is (e.g., admin scopes)
			scopes = append(scopes, s)
		}
	}
	if len(actions) > 0 {
		scopes = append(scopes, "repository:"+repo+":"+strings.Join(actions, ","))
	}
	return scopes, nil
}

func runTokenGenerate(subject, scopesStr, expiryStr, configPath, repo string) error {
	cfg, err := loadAuthConfig(configPath)
	if err != nil {
		return err
	}

	// Parse expiry (supports human-friendly units: d, w, M, y)
	var expiry time.Duration
	if expiryStr != "0" {
		expiry, err = duration.Parse(expiryStr)
		if err != nil {
			return fmt.Errorf("invalid expiry duration: %w", err)
		}
	}

	scopes, err := parseAndConvertScopes(scopesStr, repo)
	if err != nil {
		return fmt.Errorf("invalid scopes: %w", err)
	}

	// Create auth service
	log := zerowrap.New(zerowrap.Config{Level: "warn"})
	authSvc, err := createAuthServiceForCLI(cfg, log)
	if err != nil {
		return err
	}

	// Generate token
	ctx := context.Background()
	token, err := authSvc.GenerateToken(ctx, subject, scopes, expiry)
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	fmt.Println("Token generated successfully!")
	fmt.Printf("Subject: %s\n", subject)
	fmt.Printf("Scopes: %s\n", strings.Join(scopes, ", "))
	if expiry == 0 {
		fmt.Println("Expiry: never")
	} else {
		fmt.Printf("Expiry: %s\n", expiry)
	}
	fmt.Println()
	fmt.Println("Token (use as password with docker login):")
	fmt.Println(token)
	fmt.Println()
	fmt.Printf("Usage: docker login -u %s -p <token> <registry>\n", subject)

	return nil
}

// runTokenList lists all stored tokens.
func runTokenList(configPath string, out io.Writer, jsonOut bool) error {
	cfg, err := loadAuthConfig(configPath)
	if err != nil {
		return err
	}

	log := zerowrap.New(zerowrap.Config{Level: "warn"})
	authSvc, err := createAuthServiceForCLI(cfg, log)
	if err != nil {
		return err
	}

	ctx := context.Background()
	tokens, err := authSvc.ListTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tokens: %w", err)
	}

	if len(tokens) == 0 {
		if jsonOut {
			return writeJSON(out, []map[string]any{})
		}
		fmt.Println("No tokens found.")
		return nil
	}

	if jsonOut {
		payload := make([]map[string]any, 0, len(tokens))
		for _, t := range tokens {
			var expiresAt *time.Time
			if !t.ExpiresAt.IsZero() {
				expiresAt = &t.ExpiresAt
			}
			payload = append(payload, map[string]any{
				"id":          t.ID,
				"description": t.Subject,
				"scopes":      t.Scopes,
				"created_at":  t.IssuedAt,
				"expires_at":  expiresAt,
				"revoked":     t.Revoked,
			})
		}
		return writeJSON(out, payload)
	}

	fmt.Printf("%-36s  %-20s  %-20s  %-10s\n", "ID", "Subject", "Expires", "Revoked")
	fmt.Println(strings.Repeat("-", 90))

	for _, t := range tokens {
		expiry := "never"
		if !t.ExpiresAt.IsZero() {
			expiry = t.ExpiresAt.Format("2006-01-02 15:04")
		}
		revoked := "no"
		if t.Revoked {
			revoked = "yes"
		}
		fmt.Printf("%-36s  %-20s  %-20s  %-10s\n", t.ID, t.Subject, expiry, revoked)
	}

	return nil
}

// runTokenRevoke revokes a token by ID.
func runTokenRevoke(tokenID, configPath string) error {
	cfg, err := loadAuthConfig(configPath)
	if err != nil {
		return err
	}

	log := zerowrap.New(zerowrap.Config{Level: "warn"})
	authSvc, err := createAuthServiceForCLI(cfg, log)
	if err != nil {
		return err
	}

	ctx := context.Background()
	if err := authSvc.RevokeToken(ctx, tokenID); err != nil {
		return fmt.Errorf("failed to revoke token: %w", err)
	}

	fmt.Printf("Token %s has been revoked.\n", tokenID)
	return nil
}

// runTokenRevokeAll revokes all tokens.
func runTokenRevokeAll(configPath string) error {
	cfg, err := loadAuthConfig(configPath)
	if err != nil {
		return err
	}

	log := zerowrap.New(zerowrap.Config{Level: "warn"})
	authSvc, err := createAuthServiceForCLI(cfg, log)
	if err != nil {
		return err
	}

	ctx := context.Background()
	count, err := authSvc.RevokeAllTokens(ctx)
	if err != nil {
		return fmt.Errorf("failed to revoke tokens: %w", err)
	}

	if count == 0 {
		fmt.Println("No tokens to revoke.")
	} else {
		fmt.Printf("Revoked %d token(s).\n", count)
	}
	return nil
}

// cliConfig holds the configuration needed for CLI commands.
type cliConfig struct {
	Backend     domain.SecretsBackend
	DataDir     string
	TokenSecret []byte
}

// loadAuthConfig loads the configuration needed for auth CLI commands.
func loadAuthConfig(configPath string) (*cliConfig, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("server.data_dir", app.DefaultDataDir())
	v.SetDefault("auth.secrets_backend", "unsafe")

	app.ConfigureViper(v, configPath)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// Config file not found is OK for CLI commands
	}

	cfg := &cliConfig{
		DataDir: v.GetString("server.data_dir"),
	}

	// Determine backend
	switch v.GetString("auth.secrets_backend") {
	case "pass":
		cfg.Backend = domain.SecretsBackendPass
	case "sops":
		cfg.Backend = domain.SecretsBackendSops
	default:
		cfg.Backend = domain.SecretsBackendUnsafe
	}

	// Load token secret from secrets backend
	tokenSecretPath := v.GetString("auth.token_secret")
	if tokenSecretPath != "" {
		// Load actual secret from the configured backend
		ctx := context.Background()
		log := zerowrap.New(zerowrap.Config{Level: "warn"})

		switch cfg.Backend {
		case domain.SecretsBackendPass:
			provider := secrets.NewPassProvider(log)
			secret, err := provider.GetSecret(ctx, tokenSecretPath)
			if err != nil {
				return nil, fmt.Errorf("failed to load token secret from pass: %w", err)
			}
			cfg.TokenSecret = []byte(secret)
		default:
			// For unsafe backend, use the path as the secret (backwards compatible)
			cfg.TokenSecret = []byte(tokenSecretPath)
		}
	} else {
		return nil, fmt.Errorf("token_secret is required for JWT token generation; set --token-secret flag or configure auth.token_secret")
	}

	return cfg, nil
}

// createAuthServiceForCLI creates an auth service for CLI commands.
func createAuthServiceForCLI(cfg *cliConfig, log zerowrap.Logger) (*auth.Service, error) {
	// Create token store
	store, err := tokenstore.NewStore(cfg.Backend, cfg.DataDir, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create token store: %w", err)
	}

	// Create auth service config
	authConfig := auth.Config{
		Enabled:     true,
		AuthType:    domain.AuthTypeToken,
		TokenSecret: cfg.TokenSecret,
	}

	return auth.NewService(authConfig, store, log), nil
}

// newAuthStatusCmd creates the auth status command.
func newAuthStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check authentication session status",
		Long: `Verify if stored authentication session is still valid.

Shows token validity, expiry, and connection status for the active remote.

Examples:
  gordon auth status              Check status of active remote
  gordon auth status --remote prod  Check specific remote`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthStatus(cmd.Context())
		},
	}
	return cmd
}

// runAuthStatus checks authentication status for the active remote.
func runAuthStatus(ctx context.Context) error {
	resolved, err := resolveRequiredRemote(tokenFlag)
	if err != nil {
		return err
	}

	client := remote.NewClient(resolved.URL, remoteClientOptions(resolved.Token, resolved.InsecureTLS)...)

	name := resolved.DisplayName()
	fmt.Printf("Checking authentication status for '%s'...\n", name)

	status, err := client.VerifyAuth(ctx)
	if err != nil {
		return handleAuthVerifyError(err, name, resolved.URL)
	}

	displayAuthStatus(name, resolved.URL, status)
	return nil
}

// handleAuthVerifyError handles errors from auth verification.
func handleAuthVerifyError(err error, remoteName, url string) error {
	errStrLower := strings.ToLower(err.Error())

	// Connection errors
	if strings.Contains(errStrLower, "connection refused") ||
		strings.Contains(errStrLower, "no such host") ||
		strings.Contains(errStrLower, "timeout") {
		fmt.Println(styles.Theme.Bold.Render("Remote:  " + url))
		fmt.Println()
		fmt.Println(styles.Theme.Error.Render(styles.IconError + " Connection Failed"))
		fmt.Printf("Error:  %s\n\n", err.Error())
		fmt.Println(styles.Theme.Muted.Render("Check that Gordon server is running and accessible."))
		return nil
	}

	// Auth errors
	if strings.Contains(errStrLower, "401") ||
		strings.Contains(errStrLower, "unauthorized") ||
		strings.Contains(errStrLower, "invalid token") {
		fmt.Println(styles.Theme.Bold.Render("Remote:  " + url))
		fmt.Println()
		fmt.Println(styles.Theme.Error.Render(styles.IconError + " Authentication Failed"))
		fmt.Println()
		fmt.Println(styles.Theme.Muted.Render("Reason: Token is invalid or expired"))
		fmt.Println()
		fmt.Println(styles.Theme.Bold.Render("Action: Re-authenticate with:"))
		fmt.Printf("  gordon auth login --remote %s\n", remoteName)
		return nil
	}

	return fmt.Errorf("failed to verify auth: %w", err)
}

// displayAuthStatus displays authentication status information.
func displayAuthStatus(remoteName, url string, status *dto.AuthVerifyResponse) {
	fmt.Println(styles.Theme.Bold.Render("Remote:  " + remoteName + " | URL: " + url))
	fmt.Println()

	if status.Valid {
		// Valid authentication
		fmt.Println(styles.Theme.Success.Render(styles.IconSuccess + " Authenticated"))
		fmt.Println()

		if status.Subject != "" {
			fmt.Printf("%s %s\n", styles.Theme.Bold.Render("Subject:"), status.Subject)
		}

		if status.IssuedAt > 0 {
			issuedAt := time.Unix(status.IssuedAt, 0)
			fmt.Printf("%s %s\n", styles.Theme.Bold.Render("Issued:"), issuedAt.Format("2006-01-02 15:04:05"))
		}

		if status.ExpiresAt > 0 {
			expiresAt := time.Unix(status.ExpiresAt, 0)
			if expiresAt.Before(time.Now()) {
				fmt.Printf("%s %s (%s ago)\n", styles.Theme.Bold.Render("Expired:"),
					expiresAt.Format("2006-01-02 15:04:05"),
					styles.Theme.Muted.Render(time.Since(expiresAt).Round(time.Second).String()))
			} else {
				remaining := time.Until(expiresAt)
				fmt.Printf("%s %s (%s)\n", styles.Theme.Bold.Render("Expires:"),
					expiresAt.Format("2006-01-02 15:04:05"),
					styles.Theme.Muted.Render(remaining.Round(time.Second).String()))
			}
		}

		if len(status.Scopes) > 0 {
			fmt.Printf("%s %s\n", styles.Theme.Bold.Render("Scopes:"), strings.Join(status.Scopes, ", "))
		}
	} else {
		// Invalid or no authentication
		fmt.Println(styles.Theme.Error.Render(styles.IconError + " Not Authenticated"))
		fmt.Println()
		fmt.Println(styles.Theme.Muted.Render("Action: Re-authenticate with:"))
		fmt.Printf("  gordon auth login --remote %s\n", remoteName)
	}
}
