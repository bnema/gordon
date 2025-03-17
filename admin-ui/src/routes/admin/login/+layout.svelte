<script lang="ts">
  import '../../../app.css';
  let { children } = $props();
</script>

<div class="min-h-screen bg-gray-100 dark:bg-gray-900">
  <!-- Main content - Login page -->
  <main>
    {@render children()}
  </main>
</div>

<style>
  /* Add any login-specific styles here */
</style> 