import type { RequestEvent } from '@sveltejs/kit';
import { error } from '@sveltejs/kit';
import type {
  ApiResponse,
  Container,
  ContainerCreateParams,
  ContainerCreateResponse,
  ContainerUpdateParams,
  ContainerUpdateResponse,
  Image,
  ImageUploadResponse,
  LoginCredentials,
  AuthResponse,
  SessionResponse
} from '$lib/types';

/**
 * API Service for server-side communication with the Go backend
 * 
 * Authentication Flow:
 * 1. Username/Password login: Handled through this API service via the auth.login method
 * 2. OAuth login (GitHub): 
 *    - Initiated by a direct link to /admin/login/oauth/github
 *    - Handled entirely by the Go backend (see router.go)
 *    - Not processed through this API service
 *    - Note: SvelteKit server routes have been disabled in favor of the Go backend implementation
 * 3. Session validation: 
 *    - After successful login (either method), the Go backend sets a session cookie
 *    - This API service provides methods to validate and manage that session
 */
export const apiService = {
  // Base API URL - this should be configurable based on environment
  baseUrl: '/api',
  
  // Admin API URL prefix
  adminApiUrl: '/api/admin',

  // Backend URL for server-side requests (localhost)
  backendUrl: 'http://localhost:8080',

  // The name of the session cookie used by the Go backend
  sessionCookieName: 'session',

  // Server configuration
  config: {
    // Cache for the admin path to avoid repeated API calls
    _adminPathCache: null as string | null,

    // Get the admin path from the Go backend
    getAdminPath: async (event?: RequestEvent): Promise<string> => {
      // Return cached value if available
      if (apiService.config._adminPathCache) {
        console.debug('Using cached admin path:', apiService.config._adminPathCache);
        return apiService.config._adminPathCache;
      }

      // Check if we're in a build/preview context
      const isBuildTime = process.env.IS_BUILD || (process.env.NODE_ENV === 'production' && !process.env.VITE_API_URL);
      
      // Skip API call during build time
      if (isBuildTime) {
        console.debug('Build-time detected, using default admin path: /admin');
        return '/admin';
      }

      try {
        // Set a timeout for the fetch request to prevent hanging
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 3000); // 3 second timeout
        
        // Try to fetch the admin path from the server config
        let response: Response;
        
        if (event) {
          // Use event.fetch when available (preferred)
          // Add _internal parameter to prevent infinite loops
          const backendUrl = `${apiService.backendUrl}/api/config?_internal=true`;
          console.debug('Fetching admin path from backend:', backendUrl);
          
          response = await event.fetch(backendUrl, {
            headers: event.request.headers,
            signal: controller.signal
          });
        } else {
          // Fallback to using apiService.fetch which handles the baseUrl
          response = await apiService.fetch<Response>('/config?_internal=true', {
            signal: controller.signal
          });
        }
        
        // Clear the timeout
        clearTimeout(timeoutId);
        
        if (!response.ok) {
          console.warn('Failed to fetch admin path from API, using default /admin');
          return '/admin';
        }
        
        const data = await response.json();
        const adminPath = data.adminPath || '/admin';
        
        // Cache the result
        apiService.config._adminPathCache = adminPath;
        console.debug('Fetched admin path from API:', adminPath);
        
        return adminPath;
      } catch (err) {
        console.error('Error fetching admin path:', err);
        return '/admin'; // Default fallback
      }
    },
    
    // Get the log level from the Go backend
    getLogLevel: async (event?: RequestEvent): Promise<string> => {
      try {
        // Set a timeout for the fetch request to prevent hanging
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 3000); // 3 second timeout
        
        // Try to fetch the log level from the server config
        let response: Response;
        
        if (event) {
          // Use event.fetch when available (preferred)
          const backendUrl = `${apiService.backendUrl}/api/config/log-level`;
          console.debug('Fetching log level from backend:', backendUrl);
          
          response = await event.fetch(backendUrl, {
            headers: event.request.headers,
            signal: controller.signal
          });
        } else {
          // Fallback to using apiService.fetch
          response = await apiService.fetch<Response>('/config/log-level', {
            signal: controller.signal
          });
        }
        
        // Clear the timeout
        clearTimeout(timeoutId);
        
        if (!response.ok) {
          console.warn('Failed to fetch log level from API, using default info');
          return 'info';
        }
        
        const data = await response.json();
        const logLevel = data.level || 'info';
        
        console.debug('Fetched log level from API:', logLevel);
        
        return logLevel;
      } catch (err) {
        console.error('Error fetching log level:', err);
        return 'info'; // Default fallback
      }
    }
  },

  // Generic fetch wrapper with error handling for server-side
  async fetch<T>(endpoint: string, options: RequestInit = {}, event?: RequestEvent): Promise<T> {
    // For server-side requests (when event is provided), use the backendUrl
    // For client-side requests, use relative URLs
    const isServerSide = !!event;
    const baseUrlToUse = isServerSide ? this.backendUrl : '';
    const url = `${endpoint.startsWith('/') ? (isServerSide ? this.backendUrl + endpoint : endpoint) : baseUrlToUse + endpoint}`;
    
    console.debug('API request:', { url, method: options.method || 'GET', isServerSide });
    
    // Set default headers if not provided
    if (!options.headers) {
      options.headers = {
        'Content-Type': 'application/json'
      };
    }

    // If we have an event, forward the cookies for authentication
    if (event) {
      const cookies = event.request.headers.get('cookie');
      if (cookies) {
        options.headers = {
          ...options.headers,
          'Cookie': cookies
        };
      }
      
      // Forward client IP information
      const forwardedFor = event.request.headers.get('X-Forwarded-For') || event.request.headers.get('x-forwarded-for');
      const realIp = event.request.headers.get('X-Real-IP') || event.request.headers.get('x-real-ip');
      
      if (forwardedFor && options.headers && typeof options.headers === 'object') {
        (options.headers as Record<string, string>)['X-Forwarded-For'] = forwardedFor;
      }
      
      if (realIp && options.headers && typeof options.headers === 'object') {
        (options.headers as Record<string, string>)['X-Real-IP'] = realIp;
      }
    }

    // Add timeout to prevent hanging when the API is not responding
    if (!options.signal) {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => {
        console.debug('API request timed out:', url);
        controller.abort();
      }, 3000); // 3 second timeout
      
      options.signal = controller.signal;
      
      // Clean up the timeout when the request completes
      const cleanup = () => clearTimeout(timeoutId);
      options.signal.addEventListener('abort', cleanup);
    }

    try {
      let response: Response;
      
      // Use event.fetch when available to properly handle relative URLs
      if (event) {
        response = await event.fetch(url, options);
      } else {
        // Fall back to global fetch for absolute URLs only
        if (!url.startsWith('http')) {
          console.debug('Warning: Using global fetch with relative URL without event context');
        }
        response = await fetch(url, options);
      }
      
      // Handle non-2xx responses
      if (!response.ok) {
        const errorData = await response.json().catch(() => ({}));
        throw error(response.status, errorData.error || `API error: ${response.status}`);
      }
      
      // Parse JSON response
      return await response.json() as T;
    } catch (err) {
      console.error(`API request failed: ${endpoint}`, err);
      if (err instanceof Error) {
        if (err.name === 'AbortError') {
          console.debug('Request was aborted due to timeout');
          throw error(504, 'API request timed out - server may be unavailable');
        }
        throw error(500, err.message);
      }
      throw error(500, 'Unknown API error');
    }
  },

  // Container endpoints
  containers: {
    getAll: (event?: RequestEvent) => 
      apiService.fetch<Container[]>('/api/admin/containers', {}, event),
      
    getById: (id: string, event?: RequestEvent) => 
      apiService.fetch<Container>(`/api/admin/containers/${id}`, {}, event),
      
    create: (data: ContainerCreateParams, event?: RequestEvent) => 
      apiService.fetch<ContainerCreateResponse>('/api/admin/containers', {
        method: 'POST',
        body: JSON.stringify(data)
      }, event),
      
    update: (id: string, data: ContainerUpdateParams, event?: RequestEvent) => 
      apiService.fetch<ContainerUpdateResponse>(`/api/admin/containers/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data)
      }, event),
      
    delete: (id: string, event?: RequestEvent) => 
      apiService.fetch<ApiResponse>(`/api/admin/containers/${id}`, {
        method: 'DELETE'
      }, event),
      
    start: (id: string, event?: RequestEvent) => 
      apiService.fetch<ApiResponse>(`/api/admin/containers/${id}/start`, {
        method: 'POST'
      }, event),
      
    stop: (id: string, event?: RequestEvent) => 
      apiService.fetch<ApiResponse>(`/api/admin/containers/${id}/stop`, {
        method: 'POST'
      }, event),
      
    createFromImage: (imageId: string, data: ContainerCreateParams, event?: RequestEvent) => 
      apiService.fetch<ContainerCreateResponse>(`/api/admin/containers/create/${imageId}`, {
        method: 'POST',
        body: JSON.stringify(data)
      }, event),
      
    updateConfig: (id: string, data: ContainerUpdateParams, event?: RequestEvent) => 
      apiService.fetch<ContainerUpdateResponse>(`/api/admin/containers/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data)
      }, event)
  },

  // Image endpoints
  images: {
    getAll: (event?: RequestEvent) => 
      apiService.fetch<Image[]>('/api/admin/images', {}, event),
      
    delete: (id: string, event?: RequestEvent) => 
      apiService.fetch<ApiResponse>(`/api/admin/images/${id}`, {
        method: 'DELETE'
      }, event),
      
    upload: async (formData: FormData, event?: RequestEvent) => {
      const endpoint = `/api/admin/images/upload`;
      const url = event ? `${apiService.backendUrl}${endpoint}` : endpoint;
      
      // If we have an event, forward the cookies for authentication
      const options: RequestInit = { method: 'POST', body: formData };
      
      if (event) {
        const cookies = event.request.headers.get('cookie');
        if (cookies) {
          options.headers = {
            'Cookie': cookies
          };
        }
        
        // Forward client IP information
        const forwardedFor = event.request.headers.get('X-Forwarded-For') || event.request.headers.get('x-forwarded-for');
        const realIp = event.request.headers.get('X-Real-IP') || event.request.headers.get('x-real-ip');
        
        if (forwardedFor && options.headers && typeof options.headers === 'object') {
          (options.headers as Record<string, string>)['X-Forwarded-For'] = forwardedFor;
        }
        
        if (realIp && options.headers && typeof options.headers === 'object') {
          (options.headers as Record<string, string>)['X-Real-IP'] = realIp;
        }
      }
      
      console.debug('Uploading image to:', url);
      
      let response: Response;
      if (event) {
        response = await event.fetch(url, options);
      } else {
        response = await fetch(url, options);
      }
      
      if (!response.ok) {
        const errorData = await response.json().catch(() => ({}));
        throw error(response.status, errorData.error || `Upload failed: ${response.status}`);
      }
      
      return await response.json() as ImageUploadResponse;
    }
  },

  // Authentication endpoints and utilities
  auth: {
    // Login with credentials
    login: (credentials: LoginCredentials, event?: RequestEvent) => 
      apiService.fetch<AuthResponse>('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify(credentials)
      }, event),
    
    // Logout the current user - redirects to Go backend logout endpoint
    logout: () => {
      window.location.href = '/admin/logout';
      return Promise.resolve({ status: 'ok', message: 'Redirecting to logout' } as ApiResponse);
    },
    
    // Get current session information
    getSession: (event?: RequestEvent) => 
      apiService.fetch<SessionResponse>('/api/admin/auth/session', {}, event),

    // Generate a random session token (server-side)
    generateSessionToken: () => {
      const bytes = new Uint8Array(18);
      for (let i = 0; i < bytes.length; i++) {
        bytes[i] = Math.floor(Math.random() * 256);
      }
      return Array.from(bytes)
        .map(b => b.toString(16).padStart(2, '0'))
        .join('');
    },

    // Create a session by calling the Go backend API
    createSession: async (token: string, userId: string, event?: RequestEvent) => {
      try {
        // Call the Go backend API to create a session
        const browserInfo = event?.request.headers.get('user-agent') || '';
        const endpoint = '/api/admin/auth/session';
        const url = event ? `${apiService.backendUrl}${endpoint}` : endpoint;
        
        let response: Response;
        const options: RequestInit = {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json'
          },
          body: JSON.stringify({ 
            token,
            userId,
            browserInfo
          })
        };
        
        // Forward client IP and other headers if we have an event
        if (event) {
          const cookies = event.request.headers.get('cookie');
          if (cookies && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['Cookie'] = cookies;
          }
          
          const forwardedFor = event.request.headers.get('X-Forwarded-For') || event.request.headers.get('x-forwarded-for');
          const realIp = event.request.headers.get('X-Real-IP') || event.request.headers.get('x-real-ip');
          
          if (forwardedFor && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['X-Forwarded-For'] = forwardedFor;
          }
          
          if (realIp && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['X-Real-IP'] = realIp;
          }
          
          response = await event.fetch(url, options);
        } else {
          response = await fetch(url, options);
        }
        
        if (!response.ok) {
          throw error(response.status, 'Failed to create session');
        }
        
        const data = await response.json();
        return {
          id: data.sessionID,
          userId: data.accountID,
          expiresAt: new Date(data.expires)
        };
      } catch (err) {
        console.error('Error creating session:', err);
        if (err instanceof Error) {
          throw error(500, err.message);
        }
        throw error(500, 'Unknown error creating session');
      }
    },

    // Validate a session token by calling the Go backend API
    validateSessionToken: async (token: string, event?: RequestEvent) => {
      try {
        // Set a timeout for the fetch request to prevent hanging
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 3000); // 3 second timeout
        
        // In the Go backend, the session cookie contains the session information directly
        // We'll need to pass this cookie to the backend for validation
        const endpoint = '/api/admin/auth/session/validate';
        const url = event ? `${apiService.backendUrl}${endpoint}` : endpoint;
        
        const options: RequestInit = {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Cookie': `${apiService.sessionCookieName}=${token}`
          },
          signal: controller.signal
        };
        
        // If we have an event, forward the cookies for authentication
        if (event) {
          const cookies = event.request.headers.get('cookie');
          if (cookies && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['Cookie'] = cookies;
          }
          
          const forwardedFor = event.request.headers.get('X-Forwarded-For') || event.request.headers.get('x-forwarded-for');
          const realIp = event.request.headers.get('X-Real-IP') || event.request.headers.get('x-real-ip');
          
          if (forwardedFor && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['X-Forwarded-For'] = forwardedFor;
          }
          
          if (realIp && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['X-Real-IP'] = realIp;
          }
        }
        
        let response: Response;
        if (event) {
          response = await event.fetch(url, options);
        } else {
          response = await fetch(url, options);
        }
        
        // Clear the timeout
        clearTimeout(timeoutId);

        if (!response.ok) {
          return { session: null, user: null };
        }

        const data = await response.json();
        
        return {
          session: {
            id: data.sessionID,
            userId: data.accountID,
            expiresAt: new Date(data.expires)
          },
          user: {
            id: data.accountID,
            username: data.username
          }
        };
      } catch (err) {
        console.error('Error validating session token:', err);
        return { session: null, user: null };
      }
    },

    // Invalidate a session by calling the Go backend API
    invalidateSession: async (sessionId: string, event?: RequestEvent) => {
      try {
        // Set a timeout for the fetch request to prevent hanging
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 3000); // 3 second timeout
        
        // Call the Go backend API to invalidate the session
        const endpoint = '/api/admin/auth/session/invalidate';
        const url = event ? `${apiService.backendUrl}${endpoint}` : endpoint;
        
        const options: RequestInit = {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json'
          },
          body: JSON.stringify({ sessionId }),
          signal: controller.signal
        };
        
        // If we have an event, forward the cookies for authentication
        if (event) {
          const cookies = event.request.headers.get('cookie');
          if (cookies && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['Cookie'] = cookies;
          }
          
          const forwardedFor = event.request.headers.get('X-Forwarded-For') || event.request.headers.get('x-forwarded-for');
          const realIp = event.request.headers.get('X-Real-IP') || event.request.headers.get('x-real-ip');
          
          if (forwardedFor && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['X-Forwarded-For'] = forwardedFor;
          }
          
          if (realIp && options.headers && typeof options.headers === 'object') {
            (options.headers as Record<string, string>)['X-Real-IP'] = realIp;
          }
        }
        
        let response: Response;
        if (event) {
          response = await event.fetch(url, options);
        } else {
          response = await fetch(url, options);
        }
        
        // Clear the timeout
        clearTimeout(timeoutId);
        
        if (!response.ok) {
          throw error(response.status, 'Failed to invalidate session');
        }
        
        return await response.json();
      } catch (err) {
        console.error('Error invalidating session:', err);
        if (err instanceof Error) {
          throw error(500, err.message);
        }
        throw error(500, 'Unknown error invalidating session');
      }
    },

    // Set the session token cookie
    setSessionTokenCookie: (event: RequestEvent, token: string, expiresAt: Date) => {
      event.cookies.set(apiService.sessionCookieName, token, {
        path: '/',
        httpOnly: true,
        secure: process.env.NODE_ENV === 'production',
        sameSite: 'strict',
        expires: expiresAt
      });
    },

    // Delete the session token cookie
    deleteSessionTokenCookie: (event: RequestEvent) => {
      event.cookies.delete(apiService.sessionCookieName, {
        path: '/'
      });
    }
  }
};

// Export a default instance for easier imports
export default apiService;
