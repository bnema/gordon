import { writable } from 'svelte/store';
import type { Writable } from 'svelte/store';
import { api, type Image  } from '$lib/services/api';

// Create image store
interface ImageStore {
  images: Image[];
  loading: boolean;
  error: string | null;
}

function createImageStore() {
  const initialState: ImageStore = {
    images: [],
    loading: false,
    error: null
  };

  const { subscribe, set, update }: Writable<ImageStore> = writable(initialState);

  // Define store methods
  const store = {
    subscribe,
    
    // Load all images
    loadImages: async () => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const apiImages = await api.images.getAll();
        // Convert API image format to store format
        const images = apiImages.map((img: Image): Image => ({
          id: img.id,
          shortId: img.shortId,
          name: img.name,
          size: img.size,
          sizeStr: img.sizeStr,
          created: img.created, // This is already a number in the API response
          createdStr: img.createdStr,
          repoDigests: img.repoDigests,
          repoTags: img.repoTags
        }));
        
        update(state => ({ ...state, images, loading: false }));
      } catch (error) {
        console.error('Error loading images:', error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to load images' 
        }));
      }
    },
    
    // Delete an image
    deleteImage: async (id: string) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.images.delete(id);
        
        update(state => ({
          ...state,
          images: state.images.filter(image => image.id !== id),
          loading: false
        }));
        
        return result;
      } catch (error) {
        console.error(`Error deleting image ${id}:`, error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to delete image' 
        }));
        throw error;
      }
    },
    
    // Upload an image
    uploadImage: async (formData: FormData) => {
      update(state => ({ ...state, loading: true, error: null }));
      
      try {
        const result = await api.images.upload(formData);
        
        // Reload images to get the new one
        await store.loadImages();
        
        return result;
      } catch (error) {
        console.error('Error uploading image:', error);
        update(state => ({ 
          ...state, 
          loading: false, 
          error: error instanceof Error ? error.message : 'Failed to upload image' 
        }));
        throw error;
      }
    },
    
    // Reset the store
    reset: () => {
      set(initialState);
    }
  };

  return store;
}

export const imageStore = createImageStore();
