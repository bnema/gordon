function initializeVersionCheck() {
  // Dummy data for the remote version (as if fetched from a server)
  const updateStrTemplate =
    "New version %VERSION% available, consider pulling the latest image";

  const checkVersion = (versionData, currentVersion) => {
    const fetchedVersionNumber =
      versionData?.amd64?.name.match(/\d+\.\d+\.\d+/)?.[0];

    if (currentVersion !== fetchedVersionNumber) {
      const updateStr = updateStrTemplate.replace(
        "%VERSION%",
        fetchedVersionNumber,
      );
      console.debug("Update available:", updateStr);
      const updateElement = document.getElementById("update-available");
      updateElement.title = updateStr;
      updateElement.toggleAttribute("hidden", false);
      updateElement.classList.remove("hidden"); // Remove the Tailwind `hidden` class
      updateElement.classList.add("block"); // Add the Tailwind `block` class
    } else {
      console.debug(
        "No new version. Current version is up-to-date:",
        currentVersion,
      );
    }
  };

  const fetchVersionInfo = (currentVersion) => {
    fetch(`https://gordon-proxy.bamen.dev/version`)
      .then((response) => response.json())
      .then((versionData) => checkVersion(versionData, currentVersion))
      .catch((error) => console.error("Error fetching version:", error));
  };

  const init = () => {
    const currentVersionElement = document.getElementById("actual-version");
    if (!currentVersionElement) {
      console.debug("Error: #actual-version element not found. Assuming not on admin page.");
      return false; // Indicate element not found
    }
    const currentVersion = currentVersionElement.textContent.trim();
    if (!currentVersion) {
      console.debug("Dev mode detected, skipping version check.");
      return true; // Element found, but dev mode
    }
    fetchVersionInfo(currentVersion);
    return true; // Indicate element found
  };

  return init(); // Return the result of init
}

// Initialize jQuery functionality for the admin dashboard
function initializeAdminDashboard() {
  console.debug('Initializing admin dashboard with jQuery');
  
  // Check if jQuery is properly loaded
  if (typeof $ === 'undefined') {
    console.error('jQuery is not defined! Make sure jQuery is loaded before this script.');
    return;
  }
  
  // Handle edit button clicks to toggle action response visibility
  $(document).on('click', '[id^="edit-button-"]', function() {
    const id = $(this).attr('id').replace('edit-button-', '');
    const actionResponse = $(`#actionResponse-${id}`);
    
    console.debug(`Toggle visibility for actionResponse-${id}`);
    actionResponse.toggleClass('visible hidden');
    
    // Update the active state
    const isActive = $(this).data('active') === true;
    $(this).data('active', !isActive);
  });
  
  // Handle upload button click
  $(document).on('click', '#upload-button', function() {
    console.debug('Upload button clicked');
    $('#upload-image').toggleClass('visible hidden');
    $('.create-container').addClass('hidden');
  });
  
  // Handle add button click
  $(document).on('click', '[id^="add-button-img-"]', function() {
    const id = $(this).attr('id').replace('add-button-img-', '');
    console.debug(`Add button clicked for ${id}`);
    $(`#create-container-${id}`).toggleClass('visible hidden');
    $('#upload-image').addClass('hidden');
    
    // Add event listener for form submission
    $(document).on('submit', `#create-container-form-${id}`, function(e) {
      console.debug(`Form submission for container ${id}`);
      // Don't prevent default as HTMX will handle the submission
    });
  });
  
  // Handle upload submit button
  $(document).on('click', '#upload-submit-button', function() {
    console.debug('Upload submit button clicked');
    $(this).prop('disabled', true);
    
    // Re-enable the button after the HTMX request completes
    document.body.addEventListener('htmx:afterOnLoad', function() {
      $('#upload-submit-button').prop('disabled', false);
    }, { once: true });
  });
  
  // Handle success message without page reload
  $(document).on('htmx:afterSwap', function(event) {
    if ($(event.detail.target).find('#success').length > 0) {
      console.debug('Success message detected');
      
      // Instead of reloading the page, refresh the container list
      refreshContainerList();
      
      // Animate and remove the success message
      setTimeout(function() {
        $('#success').animate({ opacity: 0 }, 1000, function() {
          $(this).remove();
        });
      }, 1000);
    }
  });
  
  // Handle refresh buttons
  $(document).on('click', '#refresh-containers-button', function() {
    console.debug('Refreshing container list manually');
    refreshContainerList();
  });
  
  $(document).on('click', '#refresh-images-button', function() {
    console.debug('Refreshing image list manually');
    refreshImageList();
  });
  
  // Setup HTMX event listeners to avoid page reloads
  setupHtmxEventListeners();
  
  // Initialize notification system
  initializeNotifications();
  
  console.debug("Admin dashboard jQuery functionality initialized");
}

// Function to initialize notification system
function initializeNotifications() {
  // Create notification container if it doesn't exist
  if ($('#notification-container').length === 0) {
    $('body').append('<div id="notification-container" class="fixed top-4 right-4 z-50 flex flex-col space-y-2"></div>');
  }
  
  console.debug("Notification system initialized");
}

// Function to show a notification
function showNotification(message, type = 'success', duration = 3000) {
  const notificationId = 'notification-' + Date.now();
  const bgColor = type === 'success' ? 'bg-green-500' : type === 'error' ? 'bg-red-500' : 'bg-blue-500';
  
  const notification = $(`
    <div id="${notificationId}" class="${bgColor} text-white p-3 rounded-md shadow-md opacity-0 transition-opacity duration-300">
      ${message}
    </div>
  `);
  
  $('#notification-container').append(notification);
  
  // Fade in
  setTimeout(() => {
    $(`#${notificationId}`).css('opacity', '1');
  }, 10);
  
  // Fade out and remove after duration
  setTimeout(() => {
    $(`#${notificationId}`).css('opacity', '0');
    setTimeout(() => {
      $(`#${notificationId}`).remove();
    }, 300);
  }, duration);
  
  console.debug(`Notification shown: ${message}`);
}

// Function to refresh container list without page reload
function refreshContainerList() {
  console.debug("Refreshing container list");
  
  // Show loading indicator
  const containerManager = $('#container-manager');
  if (containerManager.length === 0) {
    console.error("Container manager element not found in DOM");
    return;
  }
  
  console.debug("Container manager element found:", containerManager[0]);
  console.debug("Container manager classes:", containerManager.attr('class'));
  
  const originalContent = containerManager.html();
  containerManager.html('<div class="text-center p-4"><span class="iconf text-3xl animate-spin inline-block">󰑐</span> Loading containers...</div>');
  
  // Ensure the container is visible
  containerManager.removeClass('hidden');
  
  $.ajax({
    url: '/htmx/container-manager',
    type: 'GET',
    success: function(data) {
      console.debug("Container data received, length:", data.length);
      $('#container-manager').html(data);
      
      // Force visibility
      $('#container-manager').removeClass('hidden');
      
      console.debug("Container list refreshed successfully");
    },
    error: function(xhr, status, error) {
      console.error("Error refreshing container list:", error);
      console.error("Status:", status);
      console.error("Response:", xhr.responseText);
      // Restore original content on error
      containerManager.html(originalContent);
    }
  });
}

// Function to refresh image list without page reload
function refreshImageList() {
  console.debug("Refreshing image list");
  
  // Show loading indicator
  const imageManager = $('#image-manager');
  if (imageManager.length === 0) {
    console.error("Image manager element not found in DOM");
    return;
  }
  
  console.debug("Image manager element found:", imageManager[0]);
  console.debug("Image manager classes:", imageManager.attr('class'));
  
  const originalContent = imageManager.html();
  imageManager.html('<div class="text-center p-4"><span class="iconf text-3xl animate-spin inline-block">󰑐</span> Loading images...</div>');
  
  $.ajax({
    url: '/htmx/image-manager',
    type: 'GET',
    success: function(data) {
      console.debug("Image data received, length:", data.length);
      $('#image-manager').html(data);
      console.debug("Image list refreshed successfully");
    },
    error: function(xhr, status, error) {
      console.error("Error refreshing image list:", error);
      console.error("Status:", status);
      console.error("Response:", xhr.responseText);
      // Restore original content on error
      imageManager.html(originalContent);
    }
  });
}

// Setup HTMX event listeners to avoid page reloads
function setupHtmxEventListeners() {
  // Listen for container actions (start, stop, edit, delete)
  document.body.addEventListener('htmx:afterOnLoad', function(event) {
    const targetId = event.detail.target.id;
    console.debug(`HTMX afterOnLoad event for target: ${targetId}`);
    
    // Remove the tab visibility fix which was interfering with DaisyUI
    // DaisyUI handles tab visibility automatically with the input[type="radio"] approach
    
    // Check if the target is an action response
    if (targetId && targetId.startsWith('actionResponse-')) {
      console.debug(`Action completed for ${targetId}`);
      
      // Show notification instead of reloading
      const responseText = $(event.detail.target).text().trim();
      if (responseText.includes('Success')) {
        showNotification('Action completed successfully', 'success');
      } else if (responseText) {
        showNotification(responseText, 'info');
      }
      
      // Refresh container list after a short delay
      setTimeout(refreshContainerList, 500);
    }
    
    // Check if the target is the image manager (after upload)
    if (targetId === 'image-manager') {
      console.debug('Image manager updated');
      
      // Show notification for successful upload
      if (event.detail.xhr && event.detail.xhr.responseText && event.detail.xhr.responseText.includes('Success')) {
        showNotification('Image uploaded successfully', 'success');
      }
      
      // Hide the upload form after successful upload
      $('#upload-image').addClass('hidden');
    }
    
    // Check if this is a container creation form response
    if (targetId && targetId.includes('create-container-')) {
      console.debug(`Container creation form response for ${targetId}`);
      const responseText = $(event.detail.target).text().trim();
      
      // If the response contains success message
      if (responseText.includes('Success') || responseText.includes('Container created successfully')) {
        showNotification('Container created successfully', 'success');
        
        // Hide the container creation form
        $(event.detail.target).closest('.create-container').addClass('hidden');
        
        // Refresh container list to show the new container
        setTimeout(refreshContainerList, 500);
      }
    }
  });
  
  // Listen for form submissions
  $(document).on('submit', 'form[hx-post^="/htmx/create-container/"]', function(event) {
    console.debug('Container creation form submitted');
    
    // Disable the submit button to prevent double submission
    $('#create-container-submit').prop('disabled', true);
    
    // We don't prevent default because HTMX needs to handle the form submission
  });
  
  // Listen for container creation
  $(document).on('htmx:afterRequest', function(event) {
    const path = event.detail.pathInfo.requestPath;
    console.debug(`HTMX afterRequest for path: ${path}`);
    
    // If this was a container creation request
    if (path && path.includes('/htmx/create-container/')) {
      console.debug('Container creation request completed');
      
      // Re-enable the submit button
      $('#create-container-submit').prop('disabled', false);
      
      // Check if the response indicates success
      const responseText = event.detail.xhr.responseText;
      if (responseText && (responseText.includes('Success') || responseText.includes('Container created successfully'))) {
        showNotification('Container created successfully', 'success');
        
        // Refresh container list after creation
        setTimeout(refreshContainerList, 500);
      } else if (responseText && responseText.includes('Error')) {
        showNotification('Error creating container: ' + responseText, 'error');
      }
    }
    
    // If this was an image deletion request
    if (path && path.includes('/htmx/image-manager/delete/')) {
      console.debug('Image deletion request completed');
      
      // Show notification
      showNotification('Image deleted successfully', 'success');
      
      // Refresh image list after deletion
      setTimeout(refreshImageList, 500);
    }
  });
  
  // Handle errors
  $(document).on('htmx:responseError', function(event) {
    console.error('HTMX response error:', event.detail.error);
    showNotification('Error: ' + (event.detail.error || 'Unknown error occurred'), 'error');
  });
  
  // Add HTMX event listeners for debugging
  document.addEventListener('htmx:beforeRequest', function(event) {
    console.debug('HTMX before request:', event.detail);
  });
  
  document.addEventListener('htmx:afterRequest', function(event) {
    console.debug('HTMX after request:', event.detail);
  });
  
  document.addEventListener('htmx:responseError', function(event) {
    console.error('HTMX response error:', event.detail);
  });
}

document.addEventListener("DOMContentLoaded", function() {
  console.debug('DOM fully loaded');
  
  // Check if jQuery is available
  if (typeof $ === 'undefined') {
    console.error('jQuery is not loaded! Some features may not work properly.');
  } else {
    console.debug('jQuery is loaded successfully');
  }
  
  // Run version check (which also checks if we're on an admin page)
  const isAdminPage = initializeVersionCheck();

  // Only initialize admin dashboard functions if jQuery is loaded AND we are on an admin page
  if (typeof $ !== 'undefined' && isAdminPage) {
    initializeAdminDashboard();
  }
});
