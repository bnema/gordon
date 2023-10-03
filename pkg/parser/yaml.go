package parser

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// // OpenYamlFile opens a YAML file and unmarshals it into the out interface
// func OpenYamlFile(fs fs.FS, filename string, out interface{}) error {
// 	// Open the YAML file
// 	file, err := fs.Open(filename)
// 	if err != nil {
// 		return fmt.Errorf("failed to open %s: %w", filename, err)
// 	}
// 	defer file.Close()

// 	// Read the content of the file
// 	content, err := io.ReadAll(file)
// 	if err != nil {
// 		return fmt.Errorf("failed to read %s: %w", filename, err)
// 	}

// 	// Unmarshal the YAML content into the out interface
// 	if err := yaml.Unmarshal(content, out); err != nil {
// 		return fmt.Errorf("failed to unmarshal %s: %w", filename, err)
// 	}

// 	return nil
// }

// OpenYamlFile opens a YAML file and unmarshals it into the out interface
func ParseYAMLFile(fs fs.FS, filename string, out interface{}, dir ...string) error {
	// Construct the full path of the YAML file
	fullPath := filename
	if len(dir) > 0 {
		fullPath = filepath.Join(dir[0], filename)
	}

	// Open the YAML file
	file, err := fs.Open(fullPath)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", fullPath, err)
	}
	defer file.Close()

	// Read the content of the file
	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", fullPath, err)
	}

	// Unmarshal the YAML content into the out interface
	if err := yaml.Unmarshal(content, out); err != nil {
		return fmt.Errorf("failed to unmarshal YAML: %w", err)
	}

	return nil
}
