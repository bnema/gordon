<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import type { LayoutData } from '$lib/types';
  
  // Get the user data from the layout data
  let { data, children } = $props<{data: LayoutData}>();
  
  // Define user type for proper TypeScript support
  interface User {
    username: string;
  }
  
  // Function to handle logout
  async function handleLogout() {
    try {
      console.debug('Sending logout request');
      const response = await fetch(`${data.adminPath}/logout`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        }
      });
      
      if (response.redirected) {
        // If the server redirected, follow that redirect
        window.location.href = response.url;
      } else {
        // Otherwise manually go to login page
        goto(`${data.adminPath}/login`);
      }
    } catch (error) {
      console.error('Error during logout:', error);
      // If there's an error, still try to go to login page
      goto(`${data.adminPath}/login`);
    }
  }
  
  // Get the current route for highlighting active links
  const currentPath = $derived($page.url.pathname);
  const isContainersActive = $derived(currentPath.includes(`${data.adminPath}/containers`));
  const isImagesActive = $derived(currentPath.includes(`${data.adminPath}/images`));
  const isStacksActive = $derived(currentPath.includes(`${data.adminPath}/stacks`));
</script>

{#if $page.url.pathname.startsWith(`${data.adminPath}/login`)}
{@render children?.()}
{:else}
  <div class="min-h-screen bg-gray-100">
    <nav class="bg-white shadow-sm">
      <div class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
        <div class="flex h-16 justify-between">
          <div class="flex">
            <div class="flex flex-shrink-0 items-center">
              <a href="{data.adminPath}" class="text-xl font-bold text-gray-900">
                Gordon Admin
              </a>
            </div>
            <div class="hidden sm:ml-6 sm:flex sm:space-x-8">
              <a 
                href="{data.adminPath}/containers" 
                class="inline-flex items-center border-b-2 px-1 pt-1 text-sm font-medium {isContainersActive ? 'border-indigo-500 text-gray-900' : 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700'}"
              >
                Containers
              </a>
              <a 
                href="{data.adminPath}/images" 
                class="inline-flex items-center border-b-2 px-1 pt-1 text-sm font-medium {isImagesActive ? 'border-indigo-500 text-gray-900' : 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700'}"
              >
                Images
              </a>
              <a 
                href="{data.adminPath}/stacks" 
                class="inline-flex items-center border-b-2 px-1 pt-1 text-sm font-medium {isStacksActive ? 'border-indigo-500 text-gray-900' : 'border-transparent text-gray-500 hover:border-gray-300 hover:text-gray-700'}"
              >
                Stacks
              </a>
            </div>
          </div>
          <div class="hidden sm:ml-6 sm:flex sm:items-center">
            <div class="flex items-center">
              {#if data.user}
                <div class="relative ml-3">
                  <div class="flex items-center">
                    <span class="mr-2 text-sm text-gray-700">{(data.user as User)?.username || 'User'}</span>
                    <button
                      onclick={handleLogout}
                      class="rounded-md bg-white px-3 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
                    >
                      Logout
                    </button>
                  </div>
                </div>
              {/if}
            </div>
          </div>
        </div>
      </div>
    </nav>

    <main class="py-10">
      <div class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
        {children()}
      </div>
    </main>
  </div>
{/if}
