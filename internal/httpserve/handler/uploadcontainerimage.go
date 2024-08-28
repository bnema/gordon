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

	// Parse the multipart form
	form, err := c.MultipartForm()
	if err != nil {
		return sendError(c, err)
	}

	// Get the file from the form
	files := form.File["file"]
	if len(files) == 0 {
		return c.String(http.StatusBadRequest, "No file uploaded")
	}
	file := files[0]

	// Open the file
	src, err := file.Open()
	if err != nil {
		return sendError(c, err)
	}
	defer src.Close()

	// Create a temporary file to store the uploaded chunks
	tempFile, err := os.CreateTemp("", "upload-*")
	if err != nil {
		return sendError(c, err)
	}
	defer tempFile.Close()

	// Copy the file chunks to the temporary file
	_, err = io.Copy(tempFile, src)
	if err != nil {
		return sendError(c, err)
	}

	// Reset the temporary file pointer
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		return sendError(c, err)
	}

	// Save the image to the storage directory
	saveInPath, err := store.SaveImageToStorage(&a.Config, file.Filename, tempFile)
	if err != nil {
		return sendError(c, err)
	}

	// Import the image into Docker
	_, err = docker.ImportImageToEngine(saveInPath)
	if err != nil {
		return fmt.Errorf("failed to import image to Docker: %v", err)
	}

	// Remove the image from the storage directory
	err = store.RemoveFromStorage(saveInPath)
	if err != nil {
		return sendError(c, err)
	}

	return c.HTML(http.StatusOK, ActionSuccess(a))
}
