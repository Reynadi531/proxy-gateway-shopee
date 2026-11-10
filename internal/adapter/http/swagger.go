package http

import (
	"io/fs"

	"pg-shopee/docs"

	"github.com/gofiber/fiber/v3"
	swaggerFiles "github.com/swaggo/files/v2"
)

func registerSwagger(app *fiber.App) {
	app.Get("/swagger", func(c fiber.Ctx) error {
		if c.Path() != "/swagger" {
			return c.Next()
		}
		return c.Redirect().Status(fiber.StatusPermanentRedirect).To("/swagger/")
	})
	resources := []struct {
		path, name, contentType string
		files                   fs.FS
	}{
		{"/swagger/", "index.html", "text/html; charset=utf-8", docs.Files},
		{"/swagger/index.html", "index.html", "text/html; charset=utf-8", docs.Files},
		{"/swagger/openapi.json", "openapi.json", "application/json", docs.Files},
		{"/swagger/swagger-init.js", "swagger-init.js", "text/javascript; charset=utf-8", docs.Files},
		{"/swagger/swagger-ui.css", "swagger-ui.css", "text/css; charset=utf-8", swaggerFiles.FS},
		{"/swagger/swagger-ui-bundle.js", "swagger-ui-bundle.js", "text/javascript; charset=utf-8", swaggerFiles.FS},
		{"/swagger/favicon-32x32.png", "favicon-32x32.png", "image/png", swaggerFiles.FS},
	}
	for _, resource := range resources {
		body, err := fs.ReadFile(resource.files, resource.name)
		if err != nil {
			panic(err)
		}
		app.Get(resource.path, func(c fiber.Ctx) error {
			c.Set("Content-Type", resource.contentType)
			c.Set("Cache-Control", "no-cache")
			c.Set("X-Content-Type-Options", "nosniff")
			return c.Send(body)
		})
	}
}
