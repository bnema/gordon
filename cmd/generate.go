package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"gordon/internal/middleware"
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate authentication tokens and secrets",
	Long:  `Generate JWT tokens, API keys, and JWT secrets for Gordon authentication`,
}

var generateSecretCmd = &cobra.Command{
	Use:   "jwt-secret",
	Short: "Generate a secure JWT secret",
	Long:  `Generate a cryptographically secure JWT secret for signing tokens`,
	Run: func(cmd *cobra.Command, args []string) {
		secret, err := middleware.GenerateJWTSecret()
		if err != nil {
			fmt.Printf("Error generating JWT secret: %v\n", err)
			return
		}
		
		fmt.Printf("Generated JWT Secret:\n")
		fmt.Printf("%s\n\n", secret)
		fmt.Printf("Add this to your gordon.toml configuration:\n")
		fmt.Printf("[auth]\n")
		fmt.Printf("enabled = true\n")
		fmt.Printf("method = \"jwt\"\n")
		fmt.Printf("jwt_secret = \"%s\"\n", secret)
	},
}

var generateTokenCmd = &cobra.Command{
	Use:   "jwt-token",
	Short: "Generate a JWT token",
	Long:  `Generate a JWT token for accessing Gordon's management API`,
	Run: func(cmd *cobra.Command, args []string) {
		username, _ := cmd.Flags().GetString("username")
		roles, _ := cmd.Flags().GetStringSlice("roles")
		jwtSecret, _ := cmd.Flags().GetString("secret")
		validFor, _ := cmd.Flags().GetDuration("valid-for")
		
		if username == "" {
			fmt.Println("Error: username is required")
			return
		}
		
		if jwtSecret == "" {
			fmt.Println("Error: JWT secret is required (use --secret flag or generate one with 'gordon generate jwt-secret')")
			return
		}
		
		token, err := middleware.GenerateJWTToken(username, roles, jwtSecret, validFor)
		if err != nil {
			fmt.Printf("Error generating JWT token: %v\n", err)
			return
		}
		
		fmt.Printf("Generated JWT Token for user '%s':\n", username)
		fmt.Printf("%s\n\n", token)
		fmt.Printf("Token expires in: %v\n", validFor)
		fmt.Printf("Roles: %v\n\n", roles)
		fmt.Printf("Use this token in API requests:\n")
		fmt.Printf("curl -H \"Authorization: Bearer %s\" http://localhost:8080/api/status\n", token)
	},
}

var generateAPIKeyCmd = &cobra.Command{
	Use:   "api-key",
	Short: "Generate a secure API key",
	Long:  `Generate a cryptographically secure API key for Gordon authentication`,
	Run: func(cmd *cobra.Command, args []string) {
		apiKey, err := middleware.GenerateSecureAPIKey()
		if err != nil {
			fmt.Printf("Error generating API key: %v\n", err)
			return
		}
		
		fmt.Printf("Generated API Key:\n")
		fmt.Printf("%s\n\n", apiKey)
		fmt.Printf("Add this to your gordon.toml configuration:\n")
		fmt.Printf("[auth]\n")
		fmt.Printf("enabled = true\n")
		fmt.Printf("method = \"api_key\"\n")
		fmt.Printf("api_key = \"%s\"\n\n", apiKey)
		fmt.Printf("Use this API key in requests:\n")
		fmt.Printf("curl -H \"X-API-Key: %s\" http://localhost:8080/api/status\n", apiKey)
		fmt.Printf("# OR\n")
		fmt.Printf("curl \"http://localhost:8080/api/status?api_key=%s\"\n", apiKey)
	},
}

func init() {
	rootCmd.AddCommand(generateCmd)
	generateCmd.AddCommand(generateSecretCmd)
	generateCmd.AddCommand(generateTokenCmd)
	generateCmd.AddCommand(generateAPIKeyCmd)
	
	// JWT token flags
	generateTokenCmd.Flags().StringP("username", "u", "", "Username for the token (required)")
	generateTokenCmd.Flags().StringSliceP("roles", "r", []string{"admin"}, "Roles for the token")
	generateTokenCmd.Flags().StringP("secret", "s", "", "JWT secret for signing (required)")
	generateTokenCmd.Flags().DurationP("valid-for", "t", 24*time.Hour, "Token validity duration")
	
	generateTokenCmd.MarkFlagRequired("username")
	generateTokenCmd.MarkFlagRequired("secret")
}