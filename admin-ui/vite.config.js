import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	define: {
		// Add a build-time flag that can be checked in the code
		'process.env.IS_BUILD': JSON.stringify(process.env.NODE_ENV === 'production')
	}
}); 