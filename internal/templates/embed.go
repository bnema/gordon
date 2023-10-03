package templates

import (
	"embed"

	"github.com/labstack/echo/v4"
)

// Embedding templates directories

//go:embed models/*
var template embed.FS
var TemplateFS = echo.MustSubFS(template, "models")
