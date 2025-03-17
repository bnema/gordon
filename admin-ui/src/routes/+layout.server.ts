import { redirect } from '@sveltejs/kit';
import type { LayoutServerLoad } from './$types';

/**
 * This server-side load function checks if the user is authenticated
 * If not, it redirects to the login page
 */
export const load: LayoutServerLoad = async ({ locals, url }) => {
  console.debug('Loading root layout data for path:', url.pathname);
  
  // Get the admin path from locals
  const adminPath = locals.adminPath || '/admin';
  
  // Skip auth check for login page and its subpaths
  if (url.pathname.startsWith(`${adminPath}/login`)) {
    console.debug('Skipping auth check for login page');
    return {
      user: null,
      adminPath
    };
  }
  
  // Check if user is authenticated
  if (!locals.user) {
    console.debug('User not authenticated, redirecting to login');
    throw redirect(302, `${adminPath}/login`);
  }
  
  // Return the user data to the layout
  console.debug('User authenticated, providing user data to layout');
  return {
    user: locals.user,
    adminPath
  };
}; 