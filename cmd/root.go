package cmd

import (
	"fmt"
	"os"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "gordon",
	Short: "Gordon - Container Registry & Deployment System",
	Long: `Gordon is a single-binary application that combines a container registry 
with an intelligent reverse proxy and automated deployment system.`,
}

func Execute() error {
	return rootCmd.Execute()
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
		} else {
			log.Fatal().Msg("config file not found - please specify with --config flag or ensure gordon.toml exists in current directory")
		}
	}
}
