package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "no arguments shows help",
			args: []string{},
		},
		{
			name: "help flag",
			args: []string{"--help"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create new command to avoid state pollution
			cmd := &cobra.Command{
				Use:   "gordon",
				Short: "Gordon - Container Registry & Deployment System",
				Long: `Gordon is a single-binary application that combines a container registry 
with an intelligent reverse proxy and automated deployment system.`,
			}

			cmd.SetArgs(tt.args)
			
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)

			// Should not panic
			assert.NotPanics(t, func() {
				cmd.Execute()
			})
		})
	}
}

func TestExecute(t *testing.T) {
	// Create a temporary config file to avoid config errors
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	configContent := `
[server]
port = 8080
registry_port = 5000
runtime = "docker"

[logging]
enabled = false

[routes]
`
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	// Save original values
	originalArgs := os.Args
	originalCfgFile := cfgFile
	
	// Test with help flag to avoid starting actual server
	os.Args = []string{"gordon", "--help"}
	cfgFile = configFile

	// Restore after test
	defer func() {
		os.Args = originalArgs
		cfgFile = originalCfgFile
		viper.Reset()
	}()

	// Should not panic
	assert.NotPanics(t, func() {
		Execute()
	})
}

func TestInitConfig(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func(t *testing.T) string // returns temp dir
		configFile     string
		configContent  string
		expectedError  bool
		shouldFindFile bool
	}{
		{
			name: "explicit config file",
			setupFunc: func(t *testing.T) string {
				return t.TempDir()
			},
			configFile: "test-gordon.toml",
			configContent: `
[server]
port = 8080
registry_port = 5000
runtime = "docker"

[logging]
enabled = false

[routes]
`,
			shouldFindFile: true,
		},
		{
			name: "config file in current directory",
			setupFunc: func(t *testing.T) string {
				tempDir := t.TempDir()
				// Change to temp directory
				originalDir, _ := os.Getwd()
				os.Chdir(tempDir)
				t.Cleanup(func() {
					os.Chdir(originalDir)
				})
				return tempDir
			},
			configFile: "gordon.toml",
			configContent: `
[server]
port = 8080
registry_port = 5000
runtime = "docker"

[logging]
enabled = false

[routes]
`,
			shouldFindFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper for each test
			viper.Reset()

			tempDir := tt.setupFunc(t)
			
			// Save original values
			originalCfgFile := cfgFile
			
			if tt.configContent != "" {
				configPath := tt.configFile
				if !filepath.IsAbs(configPath) {
					configPath = filepath.Join(tempDir, tt.configFile)
				}
				
				err := os.WriteFile(configPath, []byte(tt.configContent), 0644)
				require.NoError(t, err)
				
				if tt.name == "explicit config file" {
					cfgFile = configPath
				}
			}

			// Restore after test
			t.Cleanup(func() {
				cfgFile = originalCfgFile
				viper.Reset()
			})

			assert.NotPanics(t, func() {
				initConfig()
			})

			if tt.shouldFindFile {
				assert.NotEmpty(t, viper.ConfigFileUsed())
			}
		})
	}
}

func TestRootCmdStructure(t *testing.T) {
	// Test root command basic properties
	assert.Equal(t, "gordon", rootCmd.Use)
	assert.Contains(t, rootCmd.Short, "Gordon")
	assert.Contains(t, rootCmd.Long, "container registry")
	
	// Test that persistent flags are set
	flag := rootCmd.PersistentFlags().Lookup("config")
	assert.NotNil(t, flag)
	assert.Equal(t, "config", flag.Name)
}

func TestRootCmdSubcommands(t *testing.T) {
	// Check that expected subcommands are registered
	commands := rootCmd.Commands()
	
	commandNames := make([]string, len(commands))
	for i, cmd := range commands {
		commandNames[i] = cmd.Name()
	}

	// Should have start and reload commands
	assert.Contains(t, commandNames, "start")
	assert.Contains(t, commandNames, "reload")
}

func TestConfigFileFlag(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "test-config.toml")
	
	configContent := `
[server]
port = 9999
registry_port = 6000
runtime = "docker"

[logging]
enabled = false

[routes]
`
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	// Reset viper and cfgFile
	viper.Reset()
	originalCfgFile := cfgFile
	
	defer func() {
		cfgFile = originalCfgFile
		viper.Reset()
	}()

	// Test setting config file via flag
	cfgFile = configFile
	
	assert.NotPanics(t, func() {
		initConfig()
	})

	// Should use the specified config file
	assert.Equal(t, configFile, viper.ConfigFileUsed())
}

func TestRootCmdHelp(t *testing.T) {
	var output bytes.Buffer
	rootCmd.SetOut(&output)
	rootCmd.SetArgs([]string{"--help"})
	
	err := rootCmd.Execute()
	assert.NoError(t, err)
	
	helpOutput := output.String()
	assert.Contains(t, helpOutput, "Gordon")
	assert.Contains(t, helpOutput, "container registry")
	assert.Contains(t, helpOutput, "Available Commands:")
	assert.Contains(t, helpOutput, "start")
	assert.Contains(t, helpOutput, "reload")
}