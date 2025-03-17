<script lang="ts">
	import '../app.css';
	import { page } from '$app/stores';
	import type { LayoutData } from '$lib/types';
	import { onMount } from 'svelte';
	import { logger } from '$lib/services/logger';
	
	let { children, data } = $props<{data: LayoutData}>();

	// Navigation items
	const navItems = [
		{ href: '/', label: 'Dashboard' },
		{ href: '/containers', label: 'Containers' },
		{ href: '/images', label: 'Images' }
	];

	// Check if user is authenticated
	const isAuthenticated = $derived(!!data.user);

	$effect(() => {
		console.debug('Layout rendering with auth status:', isAuthenticated);
	});

	// Initialize logger on client-side
	onMount(async () => {
		await logger.init();
		logger.debug('Client-side logger initialized');
	});
</script>

<div class="min-h-screen bg-gray-100 dark:bg-gray-900">
	{#if isAuthenticated}
		<!-- Header - Only shown when authenticated -->
		<header class="bg-white shadow dark:bg-gray-800">
			<div class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
				<div class="flex h-16 justify-between">
					<div class="flex">
						<div class="flex flex-shrink-0 items-center">
							<span class="text-xl font-bold text-gray-900 dark:text-white">Gordon Admin</span>
						</div>
						<nav class="ml-6 flex space-x-8">
							{#each navItems as item}
								<a
									href={item.href.startsWith('/') ? data.adminPath + item.href : item.href}
									class="inline-flex items-center border-b-2 px-1 pt-1 text-sm font-medium
										{$page.url.pathname === (item.href.startsWith('/') ? data.adminPath + item.href : item.href)
										? 'border-indigo-500 text-gray-900 dark:text-white'
										: 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700 dark:text-gray-300 dark:hover:border-gray-600 dark:hover:text-gray-200'}"
								>
									{item.label}
								</a>
							{/each}
						</nav>
					</div>
					<div class="flex items-center">
						<a
							href="{data.adminPath}/profile"
							class="ml-3 inline-flex items-center rounded-md border border-transparent bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700"
						>
							Account
						</a>
					</div>
				</div>
			</div>
		</header>
	{/if}

	<!-- Main content - Always shown -->
	<main class="mx-auto max-w-7xl py-6 sm:px-6 lg:px-8">
		{@render children()}
	</main>

	{#if isAuthenticated}
		<!-- Footer - Only shown when authenticated -->
		<footer class="mt-auto bg-white shadow dark:bg-gray-800">
			<div class="mx-auto max-w-7xl px-4 py-4 sm:px-6 lg:px-8">
				<p class="text-center text-sm text-gray-500 dark:text-gray-400">
					Gordon Admin UI &copy; {new Date().getFullYear()}
				</p>
			</div>
		</footer>
	{/if}
</div>
