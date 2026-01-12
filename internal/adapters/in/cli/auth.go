package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"

	"gordon/internal/adapters/out/tokenstore"
	"gordon/internal/domain"
	"gordon/internal/usecase/auth"
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
		Long: `Generate a new JWT authentication token for registry access.

The token will be stored in the configured secrets backend and can be used
for docker login as: docker login -u <subject> -p <token>

Use --expiry=0 to generate a token that never expires (useful for CI).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenGenerate(subject, scopes, expiry, configPath)
		},
	}

	cmd.Flags().StringVar(&subject, "subject", "", "Subject/username for the token (required)")
	cmd.Flags().StringVar(&scopes, "scopes", "push,pull", "Comma-separated list of scopes")
	cmd.Flags().StringVar(&expiry, "expiry", "720h", "Token expiry duration (e.g., 720h, 24h, 0 for never)")
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
	var configPath string

	cmd := &cobra.Command{
		Use:   "revoke <token-id>",
		Short: "Revoke an authentication token",
		Long:  `Revoke a token by its ID. The token will no longer be valid for authentication.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenRevoke(args[0], configPath)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

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

	// Parse expiry
	var expiry time.Duration
	if expiryStr != "0" {
		expiry, err = time.ParseDuration(expiryStr)
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

// runPasswordHash generates a bcrypt hash for a password.
func runPasswordHash() error {
	fmt.Print("Enter password: ")

	// Read password without echo
	passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
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
	v.SetDefault("server.data_dir", "/var/lib/gordon")
	v.SetDefault("secrets.backend", "unsafe")

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("gordon")
		v.SetConfigType("yaml")
		v.AddConfigPath("/etc/gordon")
		v.AddConfigPath("$HOME/.gordon")
		v.AddConfigPath(".")
	}

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

	// Load token secret if specified
	tokenSecretPath := v.GetString("registry_auth.token_secret")
	if tokenSecretPath != "" {
		// For CLI, we generate a default secret if not available
		// In production, the secret should be loaded from the secrets backend
		cfg.TokenSecret = []byte(tokenSecretPath) // Use path as seed for now
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
