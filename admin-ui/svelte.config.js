import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	// Consult https://svelte.dev/docs/kit/integrations
	// for more information about preprocessors
	preprocess: vitePreprocess(),

	kit: {
		alias: {
			"@/*": "./src/lib/*"
		},
		adapter: adapter({
			// Output to the Go project's static directory
			pages: '../internal/webui/public',
			assets: '../internal/webui/public',
			fallback: 'index.html',
			precompress: false
		})
	}
};

export default config;
