import { redirect } from '@sveltejs/kit';
import type { RequestHandler } from '@sveltejs/kit';

export const GET: RequestHandler = async (event) => {
  console.debug('Redirecting to Go backend logout endpoint', { url: event.url.toString() });
  
  // Redirect to the Go backend logout endpoint
  // The Go backend will handle session invalidation and cookie deletion
  throw redirect(302, '/admin/logout');
};

// Also support POST for compatibility
export const POST: RequestHandler = async (event) => {
  // Just redirect to the GET handler
  return await GET(event);
}; 