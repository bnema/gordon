# Gordon Project Guidelines

## Build Commands
- Build project: `make build`
- Build CSS: `bun run build:css` or `make build-css`
- Watch CSS for changes: `bun run dev:css`
- Clean build artifacts: `make clean`
- Run rate limit test: `go run pkg/scripts/rate_limit_test/main.go --url URL`

## Code Style

### Go Guidelines
- Use tabs for indentation (4-space equivalent)
- Organize imports: standard lib first, then third-party packages
- Error handling: `if err != nil { return nil, fmt.Errorf("context: %w", err) }`
- Use structured logging with levels: `logger.Debug/Info/Warn/Error`
- Variable naming: camelCase for variables, PascalCase for exported items
- Functions should be focused and documented with comments

### JavaScript/CSS
- Use Prettier for formatting
- CSS is built with Tailwind
- Run Tailwind build with `bun run build:css`

## File Structure
- Package declaration first, followed by imports
- Type definitions before function implementations
- Methods for types should be grouped together