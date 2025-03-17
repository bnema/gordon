// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
declare global {
	namespace App {
		interface Locals {
			user: import('$lib/services/api-service.server').SessionValidationResult['user'];
			session: import('$lib/services/api-service.server').SessionValidationResult['session'];
			adminPath: string;
		}
	}
}

export {};
