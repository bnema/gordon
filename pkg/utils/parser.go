package utils

import (
	"fmt"
	"io"

	"gogs.bnema.dev/gordon-echo/internal/ui"
	"gopkg.in/yaml.v3"
)

// Yaml Parser
type Meta struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Keywords    string `yaml:"keywords"`
}

type Header struct {
	Meta       Meta   `yaml:"meta"`
	HelloWorld string `yaml:"helloworld"`
}

type Body struct {
	Header Header `yaml:"header"`
	Div    struct {
		Hello string `yaml:"hello"`
	} `yaml:"div"`
}

type LangData struct {
	Header Header `yaml:"header"`
	Body   Body   `yaml:"body"`
}

type YamlData struct {
	EN          LangData `yaml:"en"`
	FR          LangData `yaml:"fr"`
	CurrentLang LangData
}

// ReadDataFromYAML loads the data from the given YAML file
func ReadDataFromYAML() (YamlData, error) {
	var data YamlData

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

// Json Parser
