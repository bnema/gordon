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

//go:embed pkg/templating/models/*
var models embed.FS

var PublicFS = echo.MustSubFS(public, "internal/ui/public")
var TemplateFS = echo.MustSubFS(templates, "internal/ui/templates")
var ModelFS = echo.MustSubFS(models, "pkg/templating/models")
