// htmx.logAll();
document.addEventListener('htmx:afterRequest', function(event) {
  
  // Find the error message element
  var errorMessageElement = document.getElementById('error-message-img');
  
  // Find the image div element by ID (you need to get this ID dynamically)
  var imageDivID = 'img-' + event.detail.elt.id.replace('img-', '');
  var imageDivElement = document.getElementById(imageDivID);
  console.log(imageDivElement);
  
  // If it's an error, we display and hide the error message
  if (event.detail.xhr.status !== 200) {
    if (errorMessageElement && errorMessageElement.textContent.trim() !== "") {
      // Set a timeout to hide it
      setTimeout(function() {
        errorMessageElement.style.display = 'none'; // or set it to empty
        errorMessageElement.textContent = ''; // clear the text content
      }, 7000); // 3000 milliseconds = 3 seconds
    }
  } else {
    // If it's a success, we hide the image div
    if (imageDivElement) {
      setTimeout(function() {
        imageDivElement.style.display = 'none'; // Hide the div
      }, 3000); // 3000 milliseconds = 3 seconds
    }
  }
});
