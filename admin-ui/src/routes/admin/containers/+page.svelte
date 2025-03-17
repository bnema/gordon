<script lang="ts">
  import { enhance } from '$app/forms';
  import { Button } from '$lib/components/ui/button';
  import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '$lib/components/ui/table';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card';
  import { Loader2, Play, Square, Pencil, Trash2 } from '@lucide/svelte';
  import { page } from '$app/stores';
  import type { Container, PortMapping } from '$lib/types';
  
  // Get data from server load function using $props
  let { data } = $props();
  
  // Use $state for reactive state
  let containers = $state<Container[]>([]);
  let error = $state<string | null>(null);
  
  // Update containers and error when data changes
  $effect(() => {
    containers.length = 0;
    if (data.containers) {
      containers.push(...data.containers);
    }
    error = data.error || null;
    console.debug('Containers data updated:', containers.length);
  });
  
  // Track loading state for form submissions with $state
  let startingContainer = $state(false);
  let stoppingContainer = $state(false);
  let deletingContainer = $state(false);
  
  // Form submission handlers with enhanced client-side behavior
  function handleStartSubmit(id: string) {
    if (!confirm('Are you sure you want to start this container?')) return false;
    startingContainer = true;
    console.debug('Starting container:', id);
    return true;
  }
  
  function handleStopSubmit(id: string) {
    if (!confirm('Are you sure you want to stop this container?')) return false;
    stoppingContainer = true;
    console.debug('Stopping container:', id);
    return true;
  }
  
  function handleDeleteSubmit(id: string) {
    if (!confirm('Are you sure you want to delete this container?')) return false;
    deletingContainer = true;
    console.debug('Deleting container:', id);
    return true;
  }
  
  // Reset loading state after form submission
  function resetLoading() {
    startingContainer = false;
    stoppingContainer = false;
    deletingContainer = false;
    console.debug('Reset loading states');
  }
</script>

<svelte:head>
  <title>Container Management | Gordon Admin</title>
</svelte:head>

<div class="container mx-auto p-4">
  <Card>
    <CardHeader>
      <CardTitle>Container Management</CardTitle>
    </CardHeader>
    <CardContent>
      {#if !containers.length && !error}
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
              <TableHead>Ports</TableHead>
              <TableHead>Entry Point</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>State</TableHead>
              <TableHead class="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {#if containers.length === 0}
              <TableRow>
                <TableCell colspan={6} class="text-center py-8 text-muted-foreground">
                  No containers found
                </TableCell>
              </TableRow>
            {:else}
              {#each containers as container}
                <TableRow id="container-{container.id}" class="hover:bg-muted/50">
                  <TableCell class="font-medium" title={container.id}>
                    {container.name}
                  </TableCell>
                  <TableCell>
                    {(() => {
                      if (!container.ports || container.ports.length === 0) return '';
                      
                      if (typeof container.ports[0] === 'string') {
                        return container.ports.join(', ');
                      } else {
                        return container.ports.map(p => {
                          const port = p as unknown as PortMapping;
                          return `${port.PublicPort}:${port.PrivatePort}`;
                        }).join(', ');
                      }
                    })()}
                  </TableCell>
                  <TableCell>{container.proxyPort || ''}</TableCell>
                  <TableCell>{container.status} (Up since {container.createdStr})</TableCell>
                  <TableCell style="color: {container.state === 'running' ? 'green' : 'red'};">{container.state}</TableCell>
                  <TableCell class="text-right">
                    <div class="flex justify-end gap-2">
                      <form method="POST" action="?/start" use:enhance={() => {
                        const willSubmit = handleStartSubmit(container.id);
                        return ({ update, result }) => {
                          resetLoading();
                          update();
                        };
                      }}>
                        <input type="hidden" name="id" value={container.id} />
                        <Button 
                          variant="ghost" 
                          size="icon" 
                          title="Start the container"
                          type="submit"
                          disabled={startingContainer}
                        >
                          {#if startingContainer}
                            <Loader2 class="h-4 w-4 animate-spin text-blue-600" />
                          {:else}
                            <Play class="h-4 w-4 text-blue-600" />
                          {/if}
                        </Button>
                      </form>
                      
                      <Button 
                        variant="ghost" 
                        size="icon" 
                        title="Edit the container"
                        onclick={() => window.location.href = `/admin/containers/${container.id}/edit`}
                      >
                        <Pencil class="h-4 w-4 text-blue-600" />
                      </Button>
                      
                      <form method="POST" action="?/stop" use:enhance={() => {
                        const willSubmit = handleStopSubmit(container.id);
                        return ({ update, result }) => {
                          resetLoading();
                          update();
                        };
                      }}>
                        <input type="hidden" name="id" value={container.id} />
                        <Button 
                          variant="ghost" 
                          size="icon" 
                          title="Stop the container"
                          type="submit"
                          disabled={stoppingContainer}
                        >
                          {#if stoppingContainer}
                            <Loader2 class="h-4 w-4 animate-spin text-red-600" />
                          {:else}
                            <Square class="h-4 w-4 text-red-600" />
                          {/if}
                        </Button>
                      </form>
                      
                      <form method="POST" action="?/delete" use:enhance={() => {
                        const willSubmit = handleDeleteSubmit(container.id);
                        return ({ update, result }) => {
                          resetLoading();
                          update();
                        };
                      }}>
                        <input type="hidden" name="id" value={container.id} />
                        <Button 
                          variant="ghost" 
                          size="icon" 
                          title="Remove the container"
                          type="submit"
                          disabled={deletingContainer}
                        >
                          {#if deletingContainer}
                            <Loader2 class="h-4 w-4 animate-spin text-red-600" />
                          {:else}
                            <Trash2 class="h-4 w-4 text-red-600" />
                          {/if}
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
