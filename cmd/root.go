package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

// Version information set by main.go from goreleaser
var (
	BuildVersion = "dev"
	BuildCommit  = "unknown"
	BuildDate    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "gordon",
	Short: "Gordon - Container Registry & Deployment System",
	Long: `Gordon is a single-binary application that combines a container registry 
with an intelligent reverse proxy and automated deployment system.`,
}

func Execute() error {
	return rootCmd.Execute()
}

// SetVersionInfo sets the build information from main.go
func SetVersionInfo(version, commit, date string) {
	BuildVersion = version
	BuildCommit = commit
	BuildDate = date
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./gordon.toml)")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		// Search for config file in standard locations
		viper.SetConfigName("gordon")
		viper.SetConfigType("toml")
		
		// Current directory (highest priority)
		viper.AddConfigPath(".")
		
		// User config directory
		if userConfigDir, err := os.UserConfigDir(); err == nil {
			viper.AddConfigPath(userConfigDir + "/gordon")
		}
		
		// User home directory
		if homeDir, err := os.UserHomeDir(); err == nil {
			viper.AddConfigPath(homeDir + "/.gordon")
			viper.AddConfigPath(homeDir)
		}
		
		// System-wide config directories
		viper.AddConfigPath("/etc/gordon")
		viper.AddConfigPath("/usr/local/etc/gordon")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	} else {
		if cfgFile != "" {
			fmt.Fprintf(os.Stderr, "Error reading config file: %v\n", err)
		}
		// Don't fatal for version command - config not needed
		// Other commands will check for config when needed
	}
}
