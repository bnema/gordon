package webui

import (
	"fmt"
	"io/fs"

	"github.com/bnema/gordon/pkg/utils/parser"
)

type LangData map[string]interface{}

type StringsModel struct {
	Header    string `yaml:"header"`
	Subheader string `yaml:"subheader"`
}
type StringsYamlData struct {
	StringsModel StringsModel `yaml:"strings_model"`
	EN           LangData     `yaml:"en"`
	FR           LangData     `yaml:"fr"`
	CurrentLang  LangData
}

// ReadStringsDataFromYAML reads the strings.yaml file and unmarshals it into the out interface
func ReadStringsDataFromYAML(lang string, fs fs.FS, filename string, out interface{}) error {

	err := parser.ParseYAMLFile(fs, filename, out)
	if err != nil {
		return err
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
	} else {
		return fmt.Errorf("failed to assert type of out interface: %w", err)
	}

	return nil
}
