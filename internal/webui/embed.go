package webui

import (
	"embed"

	"github.com/labstack/echo/v4"
)

// Embedding the public directory
//
//go:embed public/*
var public embed.FS

var PublicFS = echo.MustSubFS(public, "internal/webui/public")
