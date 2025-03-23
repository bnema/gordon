# Templ Templates for Gordon

This directory contains [templ](https://github.com/a-h/templ) templates for the Gordon application. We're migrating from Go's html/template to templ for improved type safety and maintainability.

## Directory Structure

- `components/` - Reusable UI components
- `layouts/` - Page layouts and base templates
- `pages/` - Full page templates
  - `admin/` - Admin pages
  - `login/` - Login pages

## Working with Templates

To create or modify templates:

1. Edit the `.templ` files in the appropriate directory
2. Run `templ generate` or `make build-templ` to generate Go code
3. Use the templates in your handlers with the `TemplRenderer`

## Converting from gohtml

When converting from gohtml:

1. Create a new `.templ` file
2. Copy HTML structure from gohtml
3. Replace `{{ .varName }}` with `{ varName }`
4. Use templ's control flow syntax like `if`, `for`, etc.
5. Use `@` to invoke other components

## Example:

```templ
package components

templ Button(label string, classes string) {
  <button class={classes}>{ label }</button>
}
```

## Usage in Handlers

```go
func RenderMyPage(c echo.Context, a *server.App) error {
  renderer := render.NewTemplRenderer(a)
  return renderer.RenderTempl(c, mypage.PageTemplate("Page Title", someData))
}
```