import type { RequestEvent } from '@sveltejs/kit';
import apiService from '$lib/services/api-service.server';

export const actions = {
  upload: async (event: RequestEvent) => {
    try {
      const { request } = event;
      const formData = await request.formData();
      const file = formData.get('file') as File;
      
      if (!file) {
        return {
          success: false,
          message: 'Please select a file to upload'
        };
      }
      
      if (!file.name.endsWith('.tar')) {
        return {
          success: false,
          message: 'Only .tar files are supported'
        };
      }
      
      console.debug('Uploading image:', file.name);
      await apiService.images.upload(formData, event);
      
      return {
        success: true,
        message: 'Image uploaded successfully'
      };
    } catch (err) {
      console.error('Error uploading image:', err);
      return {
        success: false,
        message: err instanceof Error ? err.message : 'Failed to upload image'
      };
    }
  }
}; 