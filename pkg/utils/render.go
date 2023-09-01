package utils

import (
	"bytes"
	"errors"
	"html/template"
)

type Renderer struct {
	Template   *template.Template
	ParseError error
}

// Render executes the template with the specified data as the dot object
// and returns the result as plain string.
func (r *Renderer) Render(data any) (string, error) {
	if r.ParseError != nil {
		return "", r.ParseError
	}

	if r.Template == nil {
		return "", errors.New("invalid or nil template")
	}

	buf := new(bytes.Buffer)

	err := r.Template.Execute(buf, data)
	if err != nil {
		return "", errors.New("failed to execute template: " + err.Error())
	}

	return buf.String(), nil
}
