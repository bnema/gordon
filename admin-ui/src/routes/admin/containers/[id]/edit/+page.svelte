<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { Button } from '$lib/components/ui/button';
  import { Card, CardContent, CardHeader, CardTitle, CardFooter } from '$lib/components/ui/card';
  import { Textarea } from '$lib/components/ui/textarea';
  import { Loader2, ArrowLeft, Save } from '@lucide/svelte';
  import { enhance } from '$app/forms';
  import type { Container } from '$lib/types';
  
  interface PageData {
    container: Container;
  }
  
  interface ActionResult {
    success: boolean;
    message: string;
  }
  
  const containerId = $state(page.params.id);
  let containerInfo = $state('');
  let loading = $state(false);
  let saving = $state(false);
  let error = $state<string | null>(null);
  let successMessage = $state<string | null>(null);
  
  // Get data from server-side load function
  let { data, form } = $props<{ data: PageData, form?: ActionResult }>();
  
  $effect(() => {
    // Initialize container info from server data
    if (data?.container) {
      containerInfo = JSON.stringify(data.container, null, 2);
    }
    
    // Handle form submission results
    if (form?.success === false) {
      error = form.message;
      successMessage = null;
    } else if (form?.success === true) {
      successMessage = form.message;
      error = null;
    }
  });
</script>

<svelte:head>
  <title>Edit Container | Gordon Admin</title>
</svelte:head>

<div class="container mx-auto p-4">
  <Card>
    <CardHeader>
      <div class="flex items-center justify-between">
        <CardTitle>Edit Container Configuration</CardTitle>
        <Button variant="outline" size="sm" onclick={() => goto('/admin/containers')}>
          <ArrowLeft class="mr-2 h-4 w-4" />
          Back to Containers
        </Button>
      </div>
    </CardHeader>
    <CardContent>
      {#if loading}
        <div class="flex justify-center items-center h-40">
          <Loader2 class="h-8 w-8 animate-spin text-primary" />
        </div>
      {:else if error}
        <div class="bg-destructive/10 text-destructive p-4 rounded-md mb-4">
          <p>{error}</p>
        </div>
      {:else}
        {#if successMessage}
          <div class="bg-green-100 text-green-800 p-4 rounded-md mb-4">
            <p>{successMessage}</p>
          </div>
        {/if}
        
        <div class="space-y-4">
          <form method="POST" action="?/update" use:enhance={() => {
            saving = true;
            return ({ result }) => {
              saving = false;
            };
          }}>
            <Textarea 
              name="containerInfo"
              bind:value={containerInfo} 
              rows={20} 
              class="font-mono text-sm w-full"
              placeholder="Container configuration"
            />
            <div class="mt-4 flex justify-end">
              <Button 
                type="submit" 
                disabled={loading || saving}
              >
                {#if saving}
                  <Loader2 class="mr-2 h-4 w-4 animate-spin" />
                {:else}
                  <Save class="mr-2 h-4 w-4" />
                {/if}
                Save Configuration
              </Button>
            </div>
          </form>
        </div>
      {/if}
    </CardContent>
  </Card>
</div>
