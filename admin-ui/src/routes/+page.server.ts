import { redirect } from '@sveltejs/kit';
import type { PageServerLoad } from './$types';

/**
 * This server-side load function redirects to the admin dashboard if authenticated
 * The authentication check and redirect to login is already handled at the layout level
 */
export const load: PageServerLoad = async ({ locals }) => {
  console.debug('Loading root page, redirecting to admin dashboard');
  
  // Get the admin path from locals
  const adminPath = locals.adminPath || '/admin';
  
  // Redirect to the admin dashboard
  throw redirect(302, `${adminPath}/containers`);
}; 