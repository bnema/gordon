package app

import "fmt"

// ValidateConfigFile loads and statically validates an explicit configuration
// file without initializing runtime services or binding listeners.
func ValidateConfigFile(path string) error {
	if path == "" {
		return fmt.Errorf("configuration file path is required")
	}
	_, _, err := initConfig(path)
	return err
}
