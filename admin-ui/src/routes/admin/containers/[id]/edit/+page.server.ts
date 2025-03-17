import { error } from '@sveltejs/kit';
import type { RequestEvent } from '@sveltejs/kit';
import apiService from '$lib/services/api-service.server';
import type { ContainerUpdateParams } from '$lib/types';

export const load = async (event: RequestEvent) => {
  try {
    const { params } = event;
    console.debug('Fetching container data for ID:', params.id);
    if (!params.id) {
      throw error(400, 'Container ID is required');
    }
    
    const container = await apiService.containers.getById(params.id, event);
    return {
      container
    };
  } catch (err) {
    console.error('Error loading container:', err);
    throw error(500, err instanceof Error ? err.message : 'Failed to load container data');
  }
};

export const actions = {
  update: async (event: RequestEvent) => {
    try {
      const { params, request } = event;
      if (!params.id) {
        return {
          success: false,
          message: 'Container ID is required'
        };
      }
      
      const formData = await request.formData();
      const containerInfoJson = formData.get('containerInfo')?.toString() || '';
      
      console.debug('Updating container data for ID:', params.id);
      const containerData = JSON.parse(containerInfoJson) as ContainerUpdateParams;
      
      await apiService.containers.update(params.id, containerData, event);
      
      return {
        success: true,
        message: 'Container configuration saved successfully'
      };
    } catch (err) {
      console.error('Error updating container:', err);
      return {
        success: false,
        message: err instanceof Error ? err.message : 'Failed to update container'
      };
    }
  }
}; 