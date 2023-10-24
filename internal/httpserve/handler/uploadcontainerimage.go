package handler

import (
	"bytes"
	"io"
	"net/http"

	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/internal/templating/render"
	"github.com/bnema/gordon/pkg/store"
	"github.com/labstack/echo/v4"
)

const MaxUploadSize = 1024 * 1024 * 1024 // 1GB

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

	// Get the file size
	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, file)
	if err != nil {
		return sendError(c, err)
	}

	// Get the filename
	filename := header.Filename

	// Save the image to the storage directory
	_, err = store.SaveImageToStorage(&a.Config, filename, buf)
	if err != nil {
		return sendError(c, err)
	}
	return c.HTML(http.StatusOK, ActionSuccess(a))
}
