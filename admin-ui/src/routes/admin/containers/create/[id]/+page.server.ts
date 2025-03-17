import { error } from '@sveltejs/kit';
import type { RequestEvent } from '@sveltejs/kit';
import apiService from '$lib/services/api-service.server';
import type { ContainerCreateParams } from '$lib/types';

export const load = async (event: RequestEvent) => {
  try {
    const { params } = event;
    console.debug('Fetching image data for ID:', params.id);
    if (!params.id) {
      throw error(400, 'Image ID is required');
    }
    
    // Get all images and find the one with the matching ID
    const allImages = await apiService.images.getAll(event);
    const image = allImages.find(img => img.id === params.id);
    
    if (!image) {
      throw error(404, 'Image not found');
    }
    
    return {
      image
    };
  } catch (err) {
    console.error('Error loading image:', err);
    throw error(500, err instanceof Error ? err.message : 'Failed to load image data');
  }
};

export const actions = {
  create: async (event: RequestEvent) => {
    try {
      const { params, request } = event;
      if (!params.id) {
        return {
          success: false,
          message: 'Image ID is required'
        };
      }
      
      const formData = await request.formData();
      
      // Extract form data
      const containerName = formData.get('containerName')?.toString() || '';
      const containerProtocol = formData.get('containerProtocol')?.toString() || 'https';
      const containerSubdomain = formData.get('containerSubdomain')?.toString() || '';
      const containerDomain = formData.get('containerDomain')?.toString() || '';
      const ports = formData.get('ports')?.toString() || '';
      const containerPort = formData.get('containerPort')?.toString() || '';
      const volumes = formData.get('volumes')?.toString() || '';
      const environmentVariables = formData.get('environmentVariables')?.toString() || '';
      const restart = formData.get('restart')?.toString() || 'always';
      const skipProxySetup = formData.get('skipProxySetup') === 'true';
      const imageName = formData.get('imageName')?.toString() || '';
      
      // Validate required fields
      if (!containerName) {
        return {
          success: false,
          message: 'Container name is required'
        };
      }
      
      if (!containerPort) {
        return {
          success: false,
          message: 'Container proxy port is required'
        };
      }
      
      // Parse ports and volumes into arrays
      const portsArray = ports.split('\n').filter(p => p.trim() !== '');
      const volumesArray = volumes.split('\n').filter(v => v.trim() !== '');
      const envArray = environmentVariables.split('\n').filter(e => e.trim() !== '');
      
      console.debug('Creating container from image:', params.id);
      
      // Create container using apiService
      const containerData: ContainerCreateParams = {
        name: containerName,
        image: imageName,
        ports: portsArray,
        volumes: volumesArray,
        environment: envArray,
        restart: restart,
        labels: {
          protocol: containerProtocol,
          subdomain: containerSubdomain,
          domain: containerDomain,
          containerPort: containerPort,
          skipProxySetup: skipProxySetup.toString()
        }
      };
      
      await apiService.containers.createFromImage(params.id, containerData, event);
      
      return {
        success: true,
        message: 'Container created successfully'
      };
    } catch (err) {
      console.error('Error creating container:', err);
      return {
        success: false,
        message: err instanceof Error ? err.message : 'Failed to create container'
      };
    }
  }
}; 