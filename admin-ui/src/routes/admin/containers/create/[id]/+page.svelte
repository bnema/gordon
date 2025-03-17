<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { Button } from '$lib/components/ui/button';
  import { Card, CardContent, CardHeader, CardTitle, CardFooter } from '$lib/components/ui/card';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Textarea } from '$lib/components/ui/textarea';
  import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
  import { Checkbox } from '$lib/components/ui/checkbox';
  import { Alert, AlertDescription } from '$lib/components/ui/alert';
  import { Loader2, ArrowLeft, Server } from '@lucide/svelte';
  import { enhance } from '$app/forms';
  import type { Image } from '$lib/types';
  
  interface PageData {
    image: Image;
  }
  
  interface ActionResult {
    success: boolean;
    message: string;
  }
  
  const imageId = $state(page.params.id);
  let loading = $state(false);
  let creating = $state(false);
  let error = $state<string | null>(null);
  let success = $state(false);
  
  // Form data
  let containerName = $state('');
  let containerProtocol = $state<string>('https');
  let containerSubdomain = $state('');
  let containerDomain = $state('');
  let ports = $state('');
  let containerPort = $state('');
  let volumes = $state('');
  let environmentVariables = $state('');
  let restart = $state<string>('always');
  let skipProxySetup = $state(false);
  
  // Get data from server-side load function
  let { data, form } = $props<{ data: PageData, form?: ActionResult }>();
  
  $effect(() => {
    // Set default container name based on image name
    if (data?.image?.name && !containerName) {
      containerName = data.image.name.toLowerCase().replace(/[^a-z0-9]/g, '-');
    }
    
    // Handle form submission results
    if (form?.success === false) {
      error = form.message;
      success = false;
      creating = false;
    } else if (form?.success === true) {
      error = null;
      success = true;
      creating = false;
      
      // Redirect to containers page after 2 seconds
      setTimeout(() => {
        goto('/admin/containers');
      }, 2000);
    }
  });
</script>

<svelte:head>
  <title>Create Container | Gordon Admin</title>
</svelte:head>

<div class="container mx-auto p-4">
  <Card>
    <CardHeader>
      <div class="flex items-center justify-between">
        <CardTitle>Deploy Container from Image</CardTitle>
        <Button variant="outline" size="sm" onclick={() => goto('/admin/images')}>
          <ArrowLeft class="mr-2 h-4 w-4" />
          Back to Images
        </Button>
      </div>
    </CardHeader>
    <CardContent>
      {#if loading}
        <div class="flex justify-center items-center h-40">
          <Loader2 class="h-8 w-8 animate-spin text-primary" />
        </div>
      {:else if error}
        <Alert variant="destructive" class="mb-4">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      {:else if success}
        <Alert variant="default" class="bg-green-100 text-green-800 mb-4">
          <AlertDescription>
            Container created successfully! Redirecting to containers page...
          </AlertDescription>
        </Alert>
      {:else}
        <form method="POST" action="?/create" use:enhance={() => {
          creating = true;
          error = null;
          success = false;
          return ({ result }) => {
            creating = false;
          };
        }}>
          <div class="space-y-6">
            <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div>
                <Label for="image-name">Image name</Label>
                <Input 
                  id="image-name" 
                  name="imageName"
                  value={data?.image?.name || ''} 
                  readonly 
                  class="bg-muted"
                />
              </div>
              
              <div class="md:col-span-2">
                <Label for="image-id">Image ID</Label>
                <Input 
                  id="image-id" 
                  value={imageId} 
                  readonly 
                  class="bg-muted"
                />
              </div>
            </div>
            
            <div>
              <Label for="container-name">Container name</Label>
              <Input 
                id="container-name" 
                name="containerName"
                bind:value={containerName} 
                required
              />
            </div>
            
            <div class="grid grid-cols-1 md:grid-cols-12 gap-4">
              <div class="md:col-span-3">
                <Label for="container-protocol">Protocol</Label>
                <Select type="single" bind:value={containerProtocol}>
                  <SelectTrigger id="container-protocol">
                    <span>{containerProtocol}</span>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="https">https</SelectItem>
                    <SelectItem value="http">http</SelectItem>
                  </SelectContent>
                </Select>
                <input type="hidden" name="containerProtocol" value={containerProtocol} />
              </div>
              
              <div class="md:col-span-4">
                <Label for="container-subdomain">Subdomain</Label>
                <Input 
                  id="container-subdomain" 
                  name="containerSubdomain"
                  bind:value={containerSubdomain}
                />
              </div>
              
              <div class="md:col-span-5">
                <Label for="container-domain">Domain</Label>
                <Input 
                  id="container-domain" 
                  name="containerDomain"
                  bind:value={containerDomain}
                />
              </div>
            </div>
            
            <div>
              <Label for="ports">Exposed Ports (Optional)</Label>
              <p class="text-sm text-muted-foreground mb-2">
                e.g. 8080:1887/tcp (You can specify multiple ports by separating them with a comma)
              </p>
              <Input 
                id="ports" 
                name="ports"
                bind:value={ports}
              />
            </div>
            
            <div>
              <Label for="container-port">Container Proxy Port (Required)</Label>
              <p class="text-sm text-muted-foreground mb-2">
                Specify the internal container port that the proxy will route traffic to
              </p>
              <Input 
                id="container-port" 
                name="containerPort"
                bind:value={containerPort}
                required
              />
            </div>
            
            <div>
              <Label for="volumes">Volumes</Label>
              <p class="text-sm text-muted-foreground mb-2">
                e.g. ./data:/container/data
              </p>
              <Input 
                id="volumes" 
                name="volumes"
                bind:value={volumes}
              />
            </div>
            
            <div>
              <Label for="environment-variables">Environment variables</Label>
              <p class="text-sm text-muted-foreground mb-2">
                e.g. KEY=VALUE
              </p>
              <Textarea 
                id="environment-variables" 
                name="environmentVariables"
                bind:value={environmentVariables}
                rows={4}
              />
            </div>
            
            <div>
              <Label for="restart-policy">Restart Policy</Label>
              <Select type="single" bind:value={restart}>
                <SelectTrigger id="restart-policy">
                  <span>{restart}</span>
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="always">Always</SelectItem>
                  <SelectItem value="unless-stopped">Unless Stopped</SelectItem>
                  <SelectItem value="on-failure">On Failure</SelectItem>
                  <SelectItem value="no">No</SelectItem>
                </SelectContent>
              </Select>
              <input type="hidden" name="restart" value={restart} />
            </div>
            
            <div class="flex items-center space-x-2">
              <Checkbox 
                id="skip-proxy" 
                bind:checked={skipProxySetup}
              />
              <input type="hidden" name="skipProxySetup" value={skipProxySetup.toString()} />
              <Label for="skip-proxy" class="cursor-pointer">Skip proxy setup</Label>
            </div>
            
            <div class="flex justify-end">
              <Button 
                type="submit" 
                disabled={creating}
              >
                {#if creating}
                  <Loader2 class="mr-2 h-4 w-4 animate-spin" />
                  Creating...
                {:else}
                  <Server class="mr-2 h-4 w-4" />
                  Create Container
                {/if}
              </Button>
            </div>
          </div>
        </form>
      {/if}
    </CardContent>
  </Card>
</div>
