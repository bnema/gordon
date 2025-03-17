# OAuth Authentication Flow

## Implementation Decision

The OAuth authentication flow in this application is handled entirely by the Go backend, not by SvelteKit server routes.

## Disabled SvelteKit Routes

The following SvelteKit server routes have been disabled:
- `github/+server.ts.disabled` - Previously handled initiating the OAuth flow
- `callback/+server.ts.disabled` - Previously handled the OAuth callback

## Current Flow

1. User clicks "Sign in with GitHub" on the login page
2. The link points to `/admin/login/oauth/github`
3. This request is handled by the Go backend route defined in `router.go`
4. The Go backend initiates the OAuth flow by redirecting to GitHub
5. After authorization, GitHub redirects back to the callback URL
6. The Go backend handles the callback, creates a session, and sets a session cookie
7. The user is redirected to the admin dashboard

## API Service

The `api-service.server.ts` file provides methods for the SvelteKit application to communicate with the Go backend, but for OAuth, it's not directly involved in initiating the process. It's more involved in:
- Validating sessions
- Creating sessions
- Handling regular username/password login

## Future Considerations

If you want to switch back to using SvelteKit server routes for OAuth:
1. Rename the disabled files to remove the `.disabled` extension
2. Update the documentation in `api-service.server.ts`
3. Update the comment in the login page 