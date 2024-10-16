package load

import (
	"fmt"
	"io"

	"github.com/bnema/gordon/internal/server"
)

// LoadFragment loads a specific HTML fragment from the app's TemplateFS
func Fragment(a *server.App, fragmentName string) (string, error) {
	filePath := fmt.Sprintf("html/fragments/%s.gohtml", fragmentName)

	// Open the file
	file, err := a.TemplateFS.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Read the file's content
	content, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	return string(content), nil
}
