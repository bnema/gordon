package utils

import (
	"fmt"
	"io"

	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gopkg.in/yaml.v3"
)

type Data struct {
	EN          StaticData `yaml:"en"`
	FR          StaticData `yaml:"fr"`
	CurrentLang StaticData `yaml:"-"`
	Website     struct {
		Title string `yaml:"title"`
		User  string `yaml:"user"`
	} `yaml:"website"`
}

type StaticData struct {
	Title string `yaml:"title"`
	User  string `yaml:"user"`
}

// LoadDataFromYAML loads the data from the given YAML file
func LoadDataFromYAML() (Data, error) {
	var data Data

	// Open the YAML file inside the ui/components directory
	file, err := ui.TemplateFS.Open("strings.yaml")
	if err != nil {
		return data, fmt.Errorf("failed to open strings.yaml: %w", err)
	}
	defer file.Close()

	// Read the content of the file
	content, err := io.ReadAll(file)
	if err != nil {
		return data, fmt.Errorf("failed to read strings.yaml: %w", err)
	}

	// Unmarshal the YAML content into the Data struct
	if err := yaml.Unmarshal(content, &data); err != nil {
		return data, fmt.Errorf("failed to unmarshal strings.yaml: %w", err)
	}

	return data, nil
}
