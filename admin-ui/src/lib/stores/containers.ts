import { writable } from 'svelte/store';
import type { Writable } from 'svelte/store';
import { api, type Container } from '$lib/services/api';



// Container creation and update parameter types
export interface ContainerCreateData {
  name: string;
  image: string;
  ports?: string[];
  volumes?: string[];
  environment?: string[];
  labels?: Record<string, string>;
  network?: string[];
  restart?: string;
}

export interface ContainerUpdateData {
  name: string;
  image: string;
  ports?: string[];
  volumes?: string[];
  environment?: string[];
  labels?: Record<string, string>;
  network?: string[];
  restart?: string;
}

// Create container store
interface ContainerStore {
  containers: Container[];
  loading: boolean;
  error: string | null;
}

function createContainerStore() {
  const initialState: ContainerStore = {
    containers: [],
    loading: false,
    error: null
  };

  const { subscribe, set, update }: Writable<ContainerStore> = writable(initialState);

  return {
    subscribe,
    
    // Load all containers
    loadContainers: async () => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const containers = await api.containers.getAll();
        update(state => ({ ...state, containers, loading: false }));
      } catch (error) {
        console.error('Error loading containers:', error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to load containers' 
        }));
      }
    },
    
    // Start a container
    startContainer: async (id: string) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.containers.start(id);
        
        update(state => ({
          ...state,
          containers: state.containers.map(container => 
            container.id === id 
              ? { ...container, status: 'running', stateColor: 'green' }
              : container
          ),
          loading: false
        }));
        
        return result;
      } catch (error) {
        console.error(`Error starting container ${id}:`, error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to start container' 
        }));
        throw error;
      }
    },
    
    // Stop a container
    stopContainer: async (id: string) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.containers.stop(id);
        
        update(state => ({
          ...state,
          containers: state.containers.map(container => 
            container.id === id 
              ? { ...container, status: 'stopped', stateColor: 'red' }
              : container
          ),
          loading: false
        }));
        
        return result;
      } catch (error) {
        console.error(`Error stopping container ${id}:`, error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to stop container' 
        }));
        throw error;
      }
    },
    
    // Delete a container
    deleteContainer: async (id: string) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.containers.delete(id);
        
        update(state => ({
          ...state,
          containers: state.containers.filter(container => container.id !== id),
          loading: false
        }));
        
        return result;
      } catch (error) {
        console.error(`Error deleting container ${id}:`, error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to delete container' 
        }));
        throw error;
      }
    },
    
    // Create a container
    createContainer: async (imageId: string, containerData: ContainerCreateData) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.containers.createFromImage(imageId, containerData);
        
        // Reload containers to get the new one
        await api.containers.getAll().then(containers => {
          update(state => ({ ...state, containers, loading: false }));
        });
        
        return result;
      } catch (error) {
        console.error('Error creating container:', error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to create container' 
        }));
        throw error;
      }
    },
    
    // Update a container
    updateContainer: async (id: string, containerData: ContainerUpdateData) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.containers.updateConfig(id, containerData);
        
        // Reload containers to get the updated one
        await api.containers.getAll().then(containers => {
          update(state => ({ ...state, containers, loading: false }));
        });
        
        return result;
      } catch (error) {
        console.error(`Error updating container ${id}:`, error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to update container' 
        }));
        throw error;
      }
    },
    
    // Reset the store
    reset: () => {
      set(initialState);
    }
  };
}

export const containerStore = createContainerStore();
