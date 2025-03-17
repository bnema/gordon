<script lang="ts">
  import { goto } from '$app/navigation';
  import { Button } from '$lib/components/ui/button';
  import { Card, CardContent, CardHeader, CardTitle, CardFooter } from '$lib/components/ui/card';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Alert, AlertDescription } from '$lib/components/ui/alert';
  import { Loader2, ArrowLeft, Upload } from '@lucide/svelte';
  import { enhance } from '$app/forms';
  
  interface ActionResult {
    success: boolean;
    message: string;
  }
  
  let file = $state<File | null>(null);
  let uploading = $state(false);
  let error = $state<string | null>(null);
  let success = $state(false);
  let dragActive = $state(false);
  
  // Get form data from server-side action
  let { form } = $props<{ form?: ActionResult }>();
  
  $effect(() => {
    // Handle form submission results
    if (form?.success === false) {
      error = form.message;
      success = false;
      uploading = false;
    } else if (form?.success === true) {
      error = null;
      success = true;
      uploading = false;
      
      // Redirect to images page after 2 seconds
      setTimeout(() => {
        goto('/admin/images');
      }, 2000);
    }
  });
  
  function handleFileChange(event: Event) {
    const target = event.target as HTMLInputElement;
    if (target.files && target.files.length > 0) {
      file = target.files[0];
    }
  }
  
  function handleDragOver(event: DragEvent) {
    event.preventDefault();
    dragActive = true;
  }
  
  function handleDragLeave() {
    dragActive = false;
  }
  
  function handleDrop(event: DragEvent) {
    event.preventDefault();
    dragActive = false;
    
    if (event.dataTransfer?.files && event.dataTransfer.files.length > 0) {
      file = event.dataTransfer.files[0];
    }
  }
</script>

<svelte:head>
  <title>Upload Image | Gordon Admin</title>
</svelte:head>

<div class="container mx-auto p-4">
  <Card>
    <CardHeader>
      <div class="flex items-center justify-between">
        <CardTitle>Upload Container Image</CardTitle>
        <Button variant="outline" size="sm" onclick={() => goto('/admin/images')}>
          <ArrowLeft class="mr-2 h-4 w-4" />
          Back to Images
        </Button>
      </div>
    </CardHeader>
    <CardContent>
      {#if success}
        <Alert variant="default" class="bg-green-100 text-green-800 mb-4">
          <AlertDescription>
            Image uploaded successfully! Redirecting to images page...
          </AlertDescription>
        </Alert>
      {/if}
      
      {#if error}
        <Alert variant="destructive" class="mb-4">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      {/if}
      
      <form method="POST" action="?/upload" enctype="multipart/form-data" use:enhance={() => {
        uploading = true;
        error = null;
        success = false;
        return ({ result }) => {
          uploading = false;
        };
      }}>
        <div class="space-y-4">
          <div 
            class="border-2 border-dashed rounded-lg p-8 text-center transition-colors border-primary={dragActive} bg-primary/5={dragActive}"
            ondragover={handleDragOver}
            ondragleave={handleDragLeave}
            ondrop={handleDrop}
            role="button"
            aria-label="Drop zone for file upload"
            tabindex="0"
          >
            <div class="flex flex-col items-center justify-center gap-2">
              <Upload class="h-10 w-10 text-muted-foreground" />
              <h3 class="text-lg font-semibold">Drag & Drop your file here</h3>
              <p class="text-sm text-muted-foreground">or click to browse</p>
              
              <div class="mt-4">
                <Label for="file-upload" class="sr-only">Choose a file</Label>
                <Input
                  id="file-upload"
                  name="file"
                  type="file"
                  accept=".tar"
                  onchange={handleFileChange}
                  class="cursor-pointer"
                />
              </div>
              
              {#if file}
                <p class="mt-2 text-sm font-medium">Selected: {file.name}</p>
              {/if}
            </div>
          </div>
        </div>
        
        <div class="flex justify-end mt-4">
          <Button 
            type="submit"
            variant="default" 
            disabled={!file || uploading}
          >
            {#if uploading}
              <Loader2 class="mr-2 h-4 w-4 animate-spin" />
              Uploading...
            {:else}
              <Upload class="mr-2 h-4 w-4" />
              Upload Image
            {/if}
          </Button>
        </div>
      </form>
    </CardContent>
  </Card>
</div>
