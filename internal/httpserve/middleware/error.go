package middleware

import (
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
)

func ErrorHandler(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		err := next(c)
		if err != nil {
			log.Println("Error encountered:", err)
			if he, ok := err.(*echo.HTTPError); ok {
				return c.String(he.Code, he.Message.(string))
			}
			return c.String(http.StatusInternalServerError, "Internal Server Error")
		}
		return nil
	}
}
