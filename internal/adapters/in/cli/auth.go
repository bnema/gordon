package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"

	"gordon/internal/adapters/out/secrets"
	"gordon/internal/adapters/out/tokenstore"
	"gordon/internal/app"
	"gordon/internal/domain"
	"gordon/internal/usecase/auth"
	"gordon/pkg/duration"
)

// newAuthCmd creates the auth command group.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage registry authentication",
		Long:  `Commands for managing registry authentication tokens and passwords.`,
	}

	cmd.AddCommand(newAuthTokenCmd())
	cmd.AddCommand(newAuthPasswordCmd())
	cmd.AddCommand(newAuthInternalCmd())

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

  Resources: routes, secrets, config, status, * (all)
  Actions:   read, write, * (all)

  Examples:
    admin:*:*              Full admin access (recommended for CLI)
    admin:routes:read      Read-only routes access
    admin:status:read      Read-only status access

Combined examples:
  --scopes "push,pull"                Registry only (default)
  --scopes "admin:*:*"                Admin CLI only
  --scopes "push,pull,admin:*:*"      Registry + full admin

EXPIRY:

Supports human-friendly durations:
  d (days):   1d = 24 hours      w (weeks):  1w = 7 days
  M (months): 1M = 30 days       y (years):  1y = 365 days

Examples: 1y, 30d, 2w, 6M, 1y6M, 2w3d
Use --expiry=0 for a token that never expires (useful for CI).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenGenerate(subject, scopes, expiry, configPath)
		},
	}

	cmd.Flags().StringVar(&subject, "subject", "", "Subject/username for the token (required)")
	cmd.Flags().StringVar(&scopes, "scopes", "push,pull", "Comma-separated list of scopes")
	cmd.Flags().StringVar(&expiry, "expiry", "30d", "Token expiry duration (e.g., 1y, 30d, 2w, 24h, 0 for never)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

	_ = cmd.MarkFlagRequired("subject")

	return cmd
}

// newTokenListCmd creates the token list command.
func newTokenListCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all authentication tokens",
		Long:  `List all stored authentication tokens with their subjects and expiry information.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenList(configPath)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

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

// newAuthPasswordCmd creates the password subcommand group.
func newAuthPasswordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Password utilities",
		Long:  `Utilities for managing password authentication.`,
	}

	cmd.AddCommand(newPasswordHashCmd())

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

// newPasswordHashCmd creates the password hash command.
func newPasswordHashCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hash",
		Short: "Generate a bcrypt hash for a password",
		Long: `Interactively prompt for a password and output its bcrypt hash.

The hash can be stored in your secrets backend and referenced in the config:

  [registry_auth]
  type = "password"
  password_hash = "gordon/registry/password_hash"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPasswordHash()
		},
	}
}

// runTokenGenerate generates a new authentication token.
func runTokenGenerate(subject, scopesStr, expiryStr, configPath string) error {
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

	// Parse scopes
	scopes := strings.Split(scopesStr, ",")
	for i := range scopes {
		scopes[i] = strings.TrimSpace(scopes[i])
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
func runTokenList(configPath string) error {
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
		fmt.Println("No tokens found.")
		return nil
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

// runPasswordHash generates a bcrypt hash for a password.
func runPasswordHash() error {
	fmt.Print("Enter password: ")

	// Read password without echo (use os.Stdin.Fd() for better compatibility)
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		// Fallback for non-terminal input
		reader := bufio.NewReader(os.Stdin)
		password, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}
		passwordBytes = []byte(strings.TrimSpace(password))
	}
	fmt.Println()

	if len(passwordBytes) == 0 {
		return fmt.Errorf("password cannot be empty")
	}

	// Create a temporary auth service to generate the hash
	authSvc := auth.NewService(auth.Config{}, nil, zerowrap.Default())
	hash, err := authSvc.GeneratePasswordHash(string(passwordBytes))
	if err != nil {
		return fmt.Errorf("failed to generate hash: %w", err)
	}

	fmt.Println()
	fmt.Println("Bcrypt hash (store in your secrets backend):")
	fmt.Println(hash)
	fmt.Println()
	fmt.Println("Then reference the path in your config:")
	fmt.Println("  [registry_auth]")
	fmt.Println("  type = \"password\"")
	fmt.Println("  password_hash = \"gordon/registry/password_hash\"")

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
	v.SetDefault("secrets.backend", "unsafe")

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
	switch v.GetString("secrets.backend") {
	case "pass":
		cfg.Backend = domain.SecretsBackendPass
	case "sops":
		cfg.Backend = domain.SecretsBackendSops
	default:
		cfg.Backend = domain.SecretsBackendUnsafe
	}

	// Load token secret from secrets backend
	tokenSecretPath := v.GetString("registry_auth.token_secret")
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
		// Generate a default secret for CLI token generation
		cfg.TokenSecret = []byte("gordon-cli-default-secret")
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
