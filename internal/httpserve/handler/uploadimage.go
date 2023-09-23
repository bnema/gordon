package handler

import (
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/bnema/gordon/internal/app"
	"github.com/labstack/echo/v4"
)

const MaxUploadSize = 1024 * 1024 * 1024 // 1GB

// UploadImageHandler handles the /upload-image route
func UploadImageHandler(c echo.Context, a *app.App) error {
	// Set upload size limit
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, MaxUploadSize)
	if err := c.Request().ParseMultipartForm(MaxUploadSize); err != nil {
		return c.String(http.StatusRequestEntityTooLarge, "File size exceeded")
	}

	// Get the file from the form
	file, _, err := c.Request().FormFile("file")
	if err != nil {
		return sendError(c, err)
	}
	defer file.Close()

	// Check file type
	buffer := make([]byte, 512)
	if _, err := file.Read(buffer); err != nil {
		return sendError(c, err)
	}
	contentType := http.DetectContentType(buffer)
	if !strings.Contains(contentType, "application/gzip") {
		return c.String(http.StatusUnsupportedMediaType, "Invalid file type")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return sendError(c, err)
	}

	// Save to temporary file
	tmpFile, err := os.CreateTemp("/tmp", "upload-*.tar.gz")
	if err != nil {
		return sendError(c, err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := io.Copy(tmpFile, file); err != nil {
		return sendError(c, err)
	}

	// Close and flush the temporary file to make sure all content is written
	if err := tmpFile.Close(); err != nil {
		return sendError(c, err)
	}

	return c.HTML(http.StatusOK, "Success")
}
