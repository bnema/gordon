package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/out/tokenstore"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/componentauth"
	"github.com/bnema/gordon/pkg/duration"
)

const componentTokenRetrievalWarning = "Store this token securely now; it cannot be retrieved again."

// newComponentTokenCmd creates the component-token command group.
func newComponentTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "component-token",
		Short: "Manage component authentication tokens",
		Long:  "Create, list, and revoke tokens used by Gordon components.",
	}
	cmd.AddCommand(newComponentTokenCreateCmd())
	cmd.AddCommand(newComponentTokenListCmd())
	cmd.AddCommand(newComponentTokenRevokeCmd())
	return cmd
}

func newComponentTokenCreateCmd() *cobra.Command {
	var name, role, expiry, configPath string
	var scopes, scopeAliases []string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a component authentication token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComponentTokenCreate(cmd.Context(), name, domain.ComponentRole(role), append(scopes, scopeAliases...), expiry, configPath, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Component name (required)")
	cmd.Flags().StringVar(&role, "role", "", "Component role (required)")
	cmd.Flags().StringArrayVarP(&scopes, "scope", "s", nil, "Component scope (repeatable; comma-separated values supported)")
	cmd.Flags().StringArrayVar(&scopeAliases, "scopes", nil, "Component scopes (repeatable; comma-separated values supported)")
	cmd.Flags().StringVar(&expiry, "expiry", "0", "Token expiry (for example 30d, 24h, or 0 for never)")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func newComponentTokenListCmd() *cobra.Command {
	var configPath string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List component authentication tokens",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComponentTokenList(cmd.Context(), configPath, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newComponentTokenRevokeCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "revoke <key-id>",
		Short: "Revoke a component authentication token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComponentTokenRevoke(cmd.Context(), args[0], configPath, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	return cmd
}

func runComponentTokenCreate(ctx context.Context, name string, role domain.ComponentRole, rawScopes []string, expiry, configPath string, out io.Writer, jsonOut bool) error {
	config, err := loadComponentAuthConfig(configPath)
	if err != nil {
		return err
	}
	expiresAt, err := componentTokenExpiry(expiry)
	if err != nil {
		return err
	}
	service, err := createComponentAuthServiceForCLI(config, zerowrap.New(zerowrap.Config{Level: "warn"}))
	if err != nil {
		return err
	}
	result, err := service.CreateToken(ctx, componentauth.CreateRequest{
		Name:      name,
		Role:      role,
		Scopes:    componentTokenScopes(rawScopes),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return fmt.Errorf("create component token: %w", err)
	}
	if jsonOut {
		return writeJSON(out, componentTokenCreateOutput{
			Token:                        result.Token,
			Warning:                      componentTokenRetrievalWarning,
			componentTokenMetadataOutput: componentTokenMetadataOutputFrom(result.Metadata),
		})
	}
	if err := cliWriteLine(out, cliRenderSuccess("Component token created")); err != nil {
		return err
	}
	if err := writeComponentTokenMetadata(out, result.Metadata); err != nil {
		return err
	}
	if err := cliWriteLine(out, result.Token); err != nil {
		return err
	}
	return cliWriteLine(out, cliRenderWarning(componentTokenRetrievalWarning))
}

func runComponentTokenList(ctx context.Context, configPath string, out io.Writer, jsonOut bool) error {
	config, err := loadComponentAuthConfig(configPath)
	if err != nil {
		return err
	}
	service, err := createComponentAuthServiceForCLI(config, zerowrap.New(zerowrap.Config{Level: "warn"}))
	if err != nil {
		return err
	}
	metadata, err := service.ListTokenMetadata(ctx)
	if err != nil {
		return fmt.Errorf("list component tokens: %w", err)
	}
	payload := make([]componentTokenMetadataOutput, 0, len(metadata))
	for _, token := range metadata {
		payload = append(payload, componentTokenMetadataOutputFrom(token))
	}
	if jsonOut {
		return writeJSON(out, payload)
	}
	if len(metadata) == 0 {
		return cliWriteLine(out, "No component tokens found.")
	}
	for _, token := range metadata {
		if err := writeComponentTokenMetadata(out, token); err != nil {
			return err
		}
	}
	return nil
}

func runComponentTokenRevoke(ctx context.Context, keyID, configPath string, out io.Writer) error {
	config, err := loadComponentAuthConfig(configPath)
	if err != nil {
		return err
	}
	service, err := createComponentAuthServiceForCLI(config, zerowrap.New(zerowrap.Config{Level: "warn"}))
	if err != nil {
		return err
	}
	if err := service.RevokeToken(ctx, keyID); err != nil {
		return fmt.Errorf("revoke component token: %w", err)
	}
	return cliWriteLine(out, cliRenderSuccess("Component token "+keyID+" revoked."))
}

func componentTokenExpiry(value string) (time.Time, error) {
	parsed, err := duration.Parse(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid expiry: %w", err)
	}
	if parsed < 0 {
		return time.Time{}, fmt.Errorf("expiry must be positive or 0 for never")
	}
	if parsed == 0 {
		return time.Time{}, nil
	}
	return time.Now().UTC().Add(parsed), nil
}

func componentTokenScopes(values []string) []domain.ComponentScope {
	var scopes []domain.ComponentScope
	for _, value := range values {
		for _, scope := range strings.Split(value, ",") {
			scopes = append(scopes, domain.ComponentScope(strings.TrimSpace(scope)))
		}
	}
	return scopes
}

// componentAuthCLIConfig has only the backend configuration component tokens require.
type componentAuthCLIConfig struct {
	Backend domain.SecretsBackend
	DataDir string
}

// loadComponentAuthConfig deliberately does not require auth.token_secret, which is only needed by JWT commands.
func loadComponentAuthConfig(configPath string) (*componentAuthCLIConfig, error) {
	v := viper.New()
	v.SetDefault("server.data_dir", app.DefaultDataDir())
	v.SetDefault("auth.secrets_backend", "unsafe")
	app.ConfigureViper(v, configPath)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	config := &componentAuthCLIConfig{DataDir: v.GetString("server.data_dir")}
	switch v.GetString("auth.secrets_backend") {
	case "pass":
		config.Backend = domain.SecretsBackendPass
	case "sops":
		config.Backend = domain.SecretsBackendSops
	default:
		config.Backend = domain.SecretsBackendUnsafe
	}
	return config, nil
}

func createComponentAuthServiceForCLI(config *componentAuthCLIConfig, log zerowrap.Logger) (*componentauth.Service, error) {
	store, err := tokenstore.NewComponentTokenStore(config.Backend, config.DataDir, log)
	if err != nil {
		return nil, fmt.Errorf("create component token store: %w", err)
	}
	return componentauth.NewService(store, log, componentauth.Config{}), nil
}

type componentTokenMetadataOutput struct {
	KeyID      string                  `json:"key_id"`
	Prefix     string                  `json:"prefix"`
	Name       string                  `json:"name"`
	Role       domain.ComponentRole    `json:"role"`
	Scopes     []domain.ComponentScope `json:"scopes"`
	CreatedAt  time.Time               `json:"created_at"`
	ExpiresAt  *time.Time              `json:"expires_at,omitempty"`
	RevokedAt  *time.Time              `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time              `json:"last_used_at,omitempty"`
}

type componentTokenCreateOutput struct {
	Token   string `json:"token"`
	Warning string `json:"warning"`
	componentTokenMetadataOutput
}

func componentTokenMetadataOutputFrom(metadata domain.ComponentTokenMetadata) componentTokenMetadataOutput {
	return componentTokenMetadataOutput{
		KeyID:      metadata.KeyID,
		Prefix:     metadata.Prefix,
		Name:       metadata.Name,
		Role:       metadata.Role,
		Scopes:     metadata.Scopes,
		CreatedAt:  metadata.CreatedAt,
		ExpiresAt:  componentTokenTimePointer(metadata.ExpiresAt),
		RevokedAt:  componentTokenTimePointer(metadata.RevokedAt),
		LastUsedAt: componentTokenTimePointer(metadata.LastUsedAt),
	}
}

func componentTokenTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func writeComponentTokenMetadata(out io.Writer, metadata domain.ComponentTokenMetadata) error {
	if err := cliWriteLine(out, cliRenderMeta("Key ID:", metadata.KeyID)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Name:", metadata.Name)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Role:", string(metadata.Role))); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Scopes:", componentTokenScopeString(metadata.Scopes))); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Expires:", componentTokenTimeString(metadata.ExpiresAt))); err != nil {
		return err
	}
	return cliWriteLine(out, cliRenderMeta("Revoked:", componentTokenTimeString(metadata.RevokedAt)))
}

func componentTokenScopeString(scopes []domain.ComponentScope) string {
	values := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		values = append(values, string(scope))
	}
	return strings.Join(values, ", ")
}

func componentTokenTimeString(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	return value.Format(time.RFC3339)
}
