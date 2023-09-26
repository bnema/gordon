document.body.addEventListener("htmx:afterOnLoad", function(event) {
  if (
    event.target.id === "image-manager" &&
    event.detail.xhr.responseText === "Success"
  ) {
    setTimeout(function() {
      location.reload();
    }, 2000);
  }
});

function toggleButtonState(event) {
  const button = event.currentTarget;
  const isActive = button.getAttribute('data-active') === 'true';
  
  // Toggle the state
  button.setAttribute('data-active', !isActive);
  
  // Get the related component based on hx-target attribute
  const hxTarget = button.getAttribute('hx-target');
  const relatedComponent = document.querySelector(hxTarget);
  
  if (relatedComponent) {
    if (isActive) {
      relatedComponent.style.display = 'none'; // Hide
    } else {
      relatedComponent.style.display = 'block'; // Show
      
      // Adjust textarea height if it exists within the relatedComponent
      const textarea = relatedComponent.querySelector("#container_config");
      if (textarea) {
        textarea.style.height = "";  // Reset the height
        textarea.style.height = textarea.scrollHeight + "px";
      }
    }
  }
}

// Attach event listeners to buttons that needs to toggle state
document.body.addEventListener("htmx:afterSwap", function (event) {
  const buttons = document.querySelectorAll('[id^="edit-button-"], [id^="add-button-img-"]', '[id^="upload-button"]');
  buttons.forEach(button => {
    button.addEventListener('click', toggleButtonState);
  });
});

// Attach event listeners to buttons that needs to set a full location reload 
document.body.addEventListener("htmx:afterOnLoad", function (event) {
 const buttons = document.querySelectorAll('[id^="start-button-"], [id^="stop-button-"], [id^="remove-button-img-"], [id^="create-container-submit"], [id^="save-button-"]');
   buttons.forEach(button => {
    button.addEventListener('click', function(event) {
      setTimeout(function() {
      location.reload();
      }, 5000);
    });
  });
});