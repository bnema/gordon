# Gordon Admin UI Routes Checklist

This checklist tracks the verification of all routes in the Gordon Admin UI project, ensuring each route properly loads data from server files and handles form submissions correctly.

## Main Routes

- [x] `/` - Root route
  - [x] Has `+page.server.ts` that redirects to `/admin/containers`
  - [x] Has `+page.svelte` with loading UI

## Admin Routes

### Admin Layout
- [x] `/admin` - Admin layout
  - [x] Has `+layout.server.ts` for authentication
  - [x] Has `+layout.svelte` for admin UI structure
  - [x] Supports dynamic admin path from config.yml

### Admin Containers
- [x] `/admin/containers` - Container management
  - [x] Has `+page.server.ts` with load function to fetch containers
  - [x] Has `+page.svelte` with UI and form actions for container management
  - [x] Implements form actions for start/stop/delete operations

### Admin Container Creation
- [ ] `/admin/containers/create` - Create container
  - [ ] Has `+page.svelte` with form for container creation

### Admin Container Creation with Image
- [x] `/admin/containers/create/[id]` - Create container from image
  - [x] Has `+page.svelte` with form for container creation from specific image
  - [x] Properly submits form data to create container

### Admin Container Edit
- [x] `/admin/containers/[id]/edit` - Edit container
  - [x] Has `+page.svelte` with form for container editing
  - [x] Loads container data and allows updating

### Admin Images
- [x] `/admin/images` - Image management
  - [x] Has `+page.server.ts` with load function to fetch images
  - [x] Has `+page.svelte` with UI for image management
  - [x] Implements form action for delete operation

### Admin Image Upload
- [x] `/admin/images/upload` - Upload image
  - [x] Has `+page.svelte` with form for image upload
  - [x] Properly handles file upload

### Admin Login
- [x] `/admin/login` - Login page
  - [x] Has `+page.server.ts` with load and form actions for authentication
  - [x] Has `+page.svelte` with login form
  - [x] Has `+layout.svelte` for login page layout

### Admin Logout
- [x] `/admin/logout` - Logout endpoint
  - [x] Has `+server.ts` for redirecting to Go backend logout endpoint

## Notes

- All API endpoints are handled by the Go backend (see router.go)
- The SvelteKit frontend communicates with the Go backend through the api-service.server.ts service
- All routes are protected by authentication via the admin layout
- Admin path is configurable via config.yml and accessed through the API 