package utils

import (
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

// Yaml Parser
type LangData map[string]interface{}

type StringsYamlData struct {
	StringsModel StringsModel `yaml:"strings_model"`
	EN           LangData     `yaml:"en"`
	FR           LangData     `yaml:"fr"`
	CurrentLang  LangData
}

type ComposeModelData struct {
	ComposeModel ComposeModel `yaml:"compose_model"`
}

type StringsModel struct {
	Header    string `yaml:"header"`
	Subheader string `yaml:"subheader"`
}

type ComposeModel struct {
	Header    string `yaml:"header"`
	Subheader string `yaml:"subheader"`
}

// ReadDataFromYAML loads the data from the given YAML file from any filesystem
func ReadDataFromYAML(fs fs.FS, filename string, out interface{}) error {
	// Open the YAML file
	file, err := fs.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", filename, err)
	}
	defer file.Close()

	// Read the content of the file
	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", filename, err)
	}

	// Unmarshal the YAML content into the out interface
	if err := yaml.Unmarshal(content, out); err != nil {
		return fmt.Errorf("failed to unmarshal %s: %w", filename, err)
	}

	return nil
}

// PopulateDataFromYAML retrieves the data from the YAML file and sets the appropriate language data
func PopulateDataFromYAML(lang string, fs fs.FS, filename string, out interface{}) error {
	err := ReadDataFromYAML(fs, filename, out)
	if err != nil {
		return fmt.Errorf("failed to read data from YAML: %w", err)
	}

	// If the type assertion succeeds, we set the appropriate language data
	if data, ok := out.(*StringsYamlData); ok {
		switch lang {
		case "en":
			data.CurrentLang = data.EN
		case "fr":
			data.CurrentLang = data.FR
		default: // default to English if no match
			data.CurrentLang = data.EN
		}
	}

	return nil
}

// Json Parser

// ReadDataFromJSONFile loads the data from the given JSON file
func ReadDataFromJSONFile(filePath string) (string, error) {
	bytes, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// QueryJSON queries the provided JSON content based on the given gjson query and returns the results as a slice of strings
func QueryJSON(jsonContent, query string) []string {
	results := gjson.GetMany(jsonContent, query)
	var values []string
	for _, result := range results {
		values = append(values, result.String())
	}
	return values
}
