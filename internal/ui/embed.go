package ui

import (
	"embed"

	"github.com/labstack/echo/v4"
)

//go:embed public/*
var public embed.FS

//go:embed templates/*
var templates embed.FS

var PublicFS = echo.MustSubFS(public, "public")

var TemplateFS = echo.MustSubFS(templates, "templates")
