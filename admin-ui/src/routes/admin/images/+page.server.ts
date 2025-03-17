import { error } from '@sveltejs/kit';
import type { RequestEvent } from '@sveltejs/kit';
import apiService from '$lib/services/api-service.server';

export const load = async (event: RequestEvent) => {
  try {
    console.debug('Fetching all images');
    const images = await apiService.images.getAll(event);
    return {
      images
    };
  } catch (err) {
    console.error('Error loading images:', err);
    throw error(500, err instanceof Error ? err.message : 'Failed to load images data');
  }
};

export const actions = {
  delete: async (event: RequestEvent) => {
    try {
      const { request } = event;
      const formData = await request.formData();
      const imageId = formData.get('imageId')?.toString();
      
      if (!imageId) {
        return {
          success: false,
          message: 'Image ID is required'
        };
      }
      
      console.debug('Deleting image:', imageId);
      await apiService.images.delete(imageId, event);
      
      return {
        success: true,
        message: 'Image deleted successfully'
      };
    } catch (err) {
      console.error('Error deleting image:', err);
      return {
        success: false,
        message: err instanceof Error ? err.message : 'Failed to delete image'
      };
    }
  }
};
