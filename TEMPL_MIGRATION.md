# Gordon Templ Migration

This project is being migrated from using Go's html/template to [templ](https://github.com/a-h/templ) for improved type safety, maintainability, and IDE support.

## Migration Status

The following templates have been migrated:

- [x] Login page (index.gohtml → login/index.templ)
- [x] Header component (header.gohtml → components/header.templ)
- [x] Footer component (footer.gohtml → components/footer.templ)
- [x] Menu component (menu.gohtml → components/menu.templ)
- [x] Container list component (containerlist.gohtml → components/containerlist.templ)
- [x] Success component (success.gohtml → components/success.templ)
- [x] Admin dashboard (dashboard.gohtml → pages/admin/dashboard.templ)
- [x] Admin manager (manager.gohtml → pages/admin/manager.templ)
- [x] Image list component (imagelist.gohtml → components/imagelist.templ)
- [x] Upload image form (uploadimage.gohtml → components/uploadimage.templ)
- [x] Create container form (createcontainer.gohtml → components/createcontainer.templ)
- [x] Install page (install.gohtml → pages/admin/install.templ)
- [x] Edit container form (editcontainer.gohtml → components/editcontainer.templ)
- [x] Full container creation (createcontainerfull.gohtml → pages/admin/createcontainerfull.templ)
- [x] Index page (index.gohtml → pages/admin/index.templ)
- [x] User page (user.gohtml → pages/admin/user.templ)

All templates have been successfully migrated! 🎉

## Build Process

The build process has been updated to include templ generation:

1. Makefile includes a `build-templ` target that runs `templ generate`
2. GoReleaser configuration includes `templ generate` in the before hooks

## Directory Structure

- `internal/templating/models/templ/` - Root directory for templ templates
  - `components/` - Reusable UI components
  - `layouts/` - Page layouts like AdminLayout
  - `pages/` - Full page templates
    - `admin/` - Admin pages
    - `login/` - Login pages

## Theme Configuration

The default theme has been set to `gordon-dark`, which is defined in `internal/webui/public/assets/css/custom.css`.

## Handlers

New handlers have been created in `internal/httpserve/handlers/templ_handlers.go` that use the templ renderer to render the migrated templates.

## Further Work

1. ✅ Complete the migration of all templates
2. Update all handlers to use the new templ templates
3. Add error handling and loading states to components
4. Create more reusable components for forms, buttons, cards, etc.
5. Consider integrating with Go's generics for more type safety