import { fail, redirect } from '@sveltejs/kit';
import apiService from '$lib/services/api-service.server';
import type { Actions } from '@sveltejs/kit';

interface LoadParams {
	locals: { user: unknown };
	fetch: (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
}

// We'll use a simpler approach with more specific typing
export const load = async ({ locals, fetch }: LoadParams) => {
	// If user is already logged in, redirect to admin dashboard
	if (locals.user) {
		return redirect(302, '/admin/manager');
	}
	
	// Check if the database is empty by making a request to a new API endpoint
	try {
		const response = await fetch('/api/admin/auth/check-db-empty');
		const data = await response.json();
		console.debug('Database empty check result:', data);
		
		return {
			needsToken: data.isEmpty
		};
	} catch (error) {
		console.error('Error checking if database is empty:', error);
		// Default to not showing the token input if we can't determine
		return {
			needsToken: false
		};
	}
};

export const actions: Actions = {
	default: async (event) => {
		const formData = await event.request.formData();
		const username = formData.get('username');
		const password = formData.get('password');

		if (typeof username !== 'string' || !username) {
			return fail(400, { message: 'Username is required' });
		}

		if (typeof password !== 'string' || !password) {
			return fail(400, { message: 'Password is required' });
		}

		try {
			// Use the API service to login
			const authResponse = await apiService.auth.login({
				username,
				password
			});

			// Create a session token
			const sessionToken = apiService.auth.generateSessionToken();
			
			// Use the user ID from the response
			const userId = authResponse.user?.username || username;
			
			// Create a session
			const session = await apiService.auth.createSession(sessionToken, userId);
			
			// Set the session cookie
			apiService.auth.setSessionTokenCookie(event, sessionToken, session.expiresAt);
			
			// Redirect to the admin dashboard
			return redirect(302, '/admin/containers');
		} catch (error) {
			console.error('Login error:', error);
			return fail(500, { message: 'An error occurred during login' });
		}
	}
};
