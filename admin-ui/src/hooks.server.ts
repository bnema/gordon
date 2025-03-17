import type { Handle } from '@sveltejs/kit';
import { apiService } from '$lib/services/api-service.server';
import { logger } from '$lib/services/logger';

const handleAuth: Handle = async ({ event, resolve }) => {
	// Initialize logger with server log level
	await logger.init(event);
	
	logger.debug('Processing auth hook for request:', event.url.pathname);
	
	// Fetch the admin path configuration and add it to event.locals
	try {
		const adminPath = await apiService.config.getAdminPath(event);
		event.locals.adminPath = adminPath;
		logger.debug('Set adminPath in locals:', adminPath);
	} catch (error) {
		logger.error('Error fetching admin path:', error);
		// If we're in a build context or the backend is not available, use the default
		event.locals.adminPath = '/admin'; // Default fallback
		logger.debug('Using default admin path: /admin');
	}
	
	// Check if this is a login page to prevent redirect loops
	const isLoginPage = event.url.pathname.includes('/login');
	logger.debug('Is login page check:', isLoginPage, 'Path:', event.url.pathname);
	
	// If this is a login page, skip session validation to prevent redirect loops
	if (isLoginPage) {
		logger.debug('Skipping session validation for login page');
		event.locals.user = null;
		event.locals.session = null;
		return resolve(event);
	}
	
	const sessionToken = event.cookies.get(apiService.sessionCookieName);
	
	if (!sessionToken) {
		logger.debug('No session token found in cookies');
		event.locals.user = null;
		event.locals.session = null;
		return resolve(event);
	}

	try {
		// Use the apiService to validate the session token, passing the event for proper cookie forwarding
		// This will now have a timeout to prevent hanging when the API is not responding
		const { session, user } = await apiService.auth.validateSessionToken(sessionToken, event);
		
		if (session) {
			logger.debug('Valid session found for user:', user?.username);
			// Use the server-side helper to set the session cookie
			apiService.auth.setSessionTokenCookie(event, sessionToken, session.expiresAt);
		} else {
			logger.debug('Invalid or expired session token');
			// Use the server-side helper to delete the session cookie
			apiService.auth.deleteSessionTokenCookie(event);
		}

		event.locals.user = user;
		event.locals.session = session;
	} catch (error) {
		logger.error('Error validating session token:', error);
		event.locals.user = null;
		event.locals.session = null;
		
		// Check if this is a timeout error
		if (error instanceof Error && error.message.includes('timed out')) {
			logger.debug('API timeout detected, using default values');
		}
		
		// Use the server-side helper to delete the session cookie on error
		apiService.auth.deleteSessionTokenCookie(event);
	}

	return resolve(event);
};

export const handle: Handle = handleAuth;
