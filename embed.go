package embed

import (
	"embed"

	"github.com/labstack/echo/v4"
)

// Embedding the public and templates directories

//go:embed internal/ui/public/*
var public embed.FS

//go:embed internal/ui/templates/*
var templates embed.FS

// Embedding the YAML models
//
//go:embed pkg/templating/models/*
var modelsEmbed embed.FS

var PublicFS = echo.MustSubFS(public, "internal/ui/public")
var TemplateFS = echo.MustSubFS(templates, "internal/ui/templates")

// Dont need Echo for models so we are just using embed.FS
var ModelFS embed.FS = modelsEmbed
