import { redirect } from '@sveltejs/kit';
import type { LayoutServerLoad } from './$types';

/**
 * This server-side load function checks if the user is authenticated
 * If not, it redirects to the login page
 */
export const load: LayoutServerLoad = async ({ locals, url }) => {
	console.debug('Loading admin layout data for path:', url.pathname);
	
	// Get the admin path from locals
	const adminPath = locals.adminPath || '/admin';
	
	// Skip auth check for login page - handle both with and without trailing slash
	// Use includes instead of startsWith to be more flexible
	if (url.pathname.includes(`${adminPath}/login`)) {
		console.debug('Login page detected, skipping auth check');
		return {
			user: null,
			adminPath
		};
	}
	
	// Check if user is authenticated
	if (!locals.user) {
		console.debug('User not authenticated, redirecting to login');
		// Use a consistent login URL without trailing slash
		throw redirect(302, `${adminPath}/login`);
	}
	
	// Return the user data to the layout
	return {
		user: locals.user,
		adminPath
	};
};
