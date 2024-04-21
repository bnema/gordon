package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/store"
	"github.com/labstack/echo/v4"
)

// 10G
const MaxUploadSize = 10 * 1024 * 1024 * 1024 // 10GB

// UploadImageHandler handles the /upload-image route to show the form
func UploadImageGETHandler(c echo.Context, a *server.App) error {
	data := map[string]interface{}{
		"Title": "Upload Image",
	}

	rendererData, err := render.GetHTMLRenderer("html/fragments", "uploadimage.gohtml", a.TemplateFS, a)
	if err != nil {
		return sendError(c, err)
	}

	renderedHTML, err := rendererData.Render(data, a)
	if err != nil {
		return err
	}
	return c.HTML(200, renderedHTML)
}

// UploadImageHandler handles the /upload-image
func UploadImagePOSTHandler(c echo.Context, a *server.App) error {
	// Set upload size limit
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, MaxUploadSize)
	if err := c.Request().ParseMultipartForm(MaxUploadSize); err != nil {
		return c.String(http.StatusRequestEntityTooLarge, "File size exceeded")
	}

	// Get the file from the form
	file, header, err := c.Request().FormFile("file")
	if err != nil {
		return sendError(c, err)
	}
	defer file.Close()

	// Get the filename
	filename := header.Filename

	// Create a temporary file to store the uploaded chunks
	tempFile, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return sendError(c, err)
	}
	defer tempFile.Close()

	// Copy the file chunks to the temporary file
	_, err = io.Copy(tempFile, file)
	if err != nil {
		return sendError(c, err)
	}

	// Reset the temporary file pointer
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		return sendError(c, err)
	}

	// Save the image to the storage directory
	saveInPath, err := store.SaveImageToStorage(&a.Config, filename, tempFile)
	if err != nil {
		return sendError(c, err)
	}

	// Import the image into Docker
	err = docker.ImportImageToEngine(saveInPath)
	if err != nil {
		return fmt.Errorf("failed to import image to Docker: %v", err)
	}

	// Remove the image from the storage directory
	err = store.RemoveFromStorage()
	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(http.StatusOK, ActionSuccess(a))
}
