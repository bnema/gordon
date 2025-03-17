import { fail } from '@sveltejs/kit';
import { apiService } from '$lib/services/api-service.server';
import type { PageServerLoad, Actions } from './$types';

export const load = (async ({ locals, request }: Parameters<PageServerLoad>[0]) => {
  try {
    const containers = await apiService.containers.getAll();
    return {
      containers,
      error: null
    };
  } catch (error) {
    console.error('Error loading containers:', error);
    return {
      containers: [],
      error: error instanceof Error ? error.message : 'Failed to load containers'
    };
  }
}) satisfies PageServerLoad;

export const actions = {
  start: async ({ request }: Parameters<Actions['start']>[0]) => {
    const formData = await request.formData();
    const id = formData.get('id');

    if (typeof id !== 'string' || !id) {
      return fail(400, { success: false, message: 'Container ID is required' });
    }

    try {
      await apiService.containers.start(id);
      return { success: true };
    } catch (error) {
      console.error('Error starting container:', error);
      return fail(500, { 
        success: false, 
        message: error instanceof Error ? error.message : 'Failed to start container' 
      });
    }
  },

  stop: async ({ request }: Parameters<Actions['stop']>[0]) => {
    const formData = await request.formData();
    const id = formData.get('id');

    if (typeof id !== 'string' || !id) {
      return fail(400, { success: false, message: 'Container ID is required' });
    }

    try {
      await apiService.containers.stop(id);
      return { success: true };
    } catch (error) {
      console.error('Error stopping container:', error);
      return fail(500, { 
        success: false, 
        message: error instanceof Error ? error.message : 'Failed to stop container' 
      });
    }
  },

  delete: async ({ request }: Parameters<Actions['delete']>[0]) => {
    const formData = await request.formData();
    const id = formData.get('id');

    if (typeof id !== 'string' || !id) {
      return fail(400, { success: false, message: 'Container ID is required' });
    }

    try {
      await apiService.containers.delete(id);
      return { success: true };
    } catch (error) {
      console.error('Error deleting container:', error);
      return fail(500, { 
        success: false, 
        message: error instanceof Error ? error.message : 'Failed to delete container' 
      });
    }
  }
} satisfies Actions;
