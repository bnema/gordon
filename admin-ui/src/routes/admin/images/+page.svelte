<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { Button } from '$lib/components/ui/button';
  import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '$lib/components/ui/card';
  import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '$lib/components/ui/table';
  import { Loader2, Upload, Plus, Trash2, Play } from '@lucide/svelte';
  import { goto } from '$app/navigation';
  import { enhance } from '$app/forms';
  import type { Image } from '$lib/types';

  interface PageData {
    images: Image[];
  }
  
  interface ActionResult {
    success: boolean;
    message: string;
  }

  let loading = $state(false);
  let error = $state<string | null>(null);
  let pollingInterval = $state<number | null>(null);
  let refreshing = $state(false);

  // Get data from server-side load function
  let { data, form } = $props<{ data: PageData, form?: ActionResult }>();
  
  $effect(() => {
    // Handle form submission results
    if (form?.success === false) {
      error = form.message;
    } else if (form?.success === true) {
      error = null;
      // Refresh the page to get updated data
      window.location.reload();
    }
  });

  function startPolling() {
    pollingInterval = window.setInterval(() => {
      refreshing = true;
      // Refresh the page to get updated data
      window.location.reload();
    }, 30000); // Poll every 30 seconds
  }

  function stopPolling() {
    if (pollingInterval) {
      clearInterval(pollingInterval);
      pollingInterval = null;
    }
  }

  function navigateToUpload() {
    goto('/admin/images/upload');
  }

  function navigateToCreateContainer(imageId: string) {
    goto(`/admin/containers/create/${imageId}`);
  }

  onMount(() => {
    startPolling();
  });

  onDestroy(() => {
    stopPolling();
  });
</script>

<svelte:head>
  <title>Container Images | Gordon Admin</title>
</svelte:head>

<div class="container mx-auto p-4">
  <Card>
    <CardHeader>
      <div class="flex items-center justify-between">
        <div>
          <CardTitle>Image Management</CardTitle>
          <CardDescription>Manage your container images</CardDescription>
        </div>
        <Button onclick={() => navigateToUpload()}>
          <Upload class="mr-2 h-4 w-4" />
          Upload Image
        </Button>
      </div>
    </CardHeader>
    <CardContent>
      {#if loading}
        <div class="flex justify-center items-center h-40">
          <Loader2 class="h-8 w-8 animate-spin text-primary" />
        </div>
      {:else if error}
        <div class="bg-destructive/10 text-destructive p-4 rounded-md">
          <p>{error}</p>
          <Button variant="outline" class="mt-2" onclick={() => window.location.reload()}>Retry</Button>
        </div>
      {:else}
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>ID</TableHead>
              <TableHead>Size</TableHead>
              <TableHead>Created</TableHead>
              <TableHead class="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {#if !data.images || data.images.length === 0}
              <TableRow>
                <TableCell colspan={5} class="text-center py-4">
                  <p class="text-muted-foreground">No container images found.</p>
                  <p class="text-sm text-muted-foreground mt-2">Upload an image to get started.</p>
                </TableCell>
              </TableRow>
            {:else}
              {#each data.images as image}
                <TableRow>
                  <TableCell>{image.name || '<none>'}</TableCell>
                  <TableCell>{image.shortId}</TableCell>
                  <TableCell>{image.sizeStr}</TableCell>
                  <TableCell>{image.createdStr}</TableCell>
                  <TableCell class="text-right">
                    <div class="flex justify-end space-x-2">
                      <Button 
                        variant="outline" 
                        size="sm"
                        onclick={() => navigateToCreateContainer(image.id)}
                      >
                        <Play class="h-4 w-4" />
                        <span class="sr-only">Deploy</span>
                      </Button>
                      
                      <form method="POST" action="?/delete" use:enhance>
                        <input type="hidden" name="imageId" value={image.id} />
                        <Button 
                          variant="destructive" 
                          size="sm"
                          type="submit"
                          onclick={(e) => {
                            if (!confirm('Are you sure you want to delete this container image?')) {
                              e.preventDefault();
                            }
                          }}
                        >
                          <Trash2 class="h-4 w-4" />
                          <span class="sr-only">Delete</span>
                        </Button>
                      </form>
                    </div>
                  </TableCell>
                </TableRow>
              {/each}
            {/if}
          </TableBody>
        </Table>
      {/if}
    </CardContent>
  </Card>
</div>
