document.addEventListener('DOMContentLoaded', function() {
  document.body.addEventListener('htmx:responseError', function(event) {
    const errorDetail = event.detail;
    const errorType = errorDetail && errorDetail.xhr ? errorDetail.xhr.getResponseHeader("X-Error-Type") : 'generic'; // Fallback to 'generic' if not set
    const errorDiv = document.querySelector(`div[data-error-type="${errorType}"]`);
    if (errorDiv) {
      if (errorDetail && errorDetail.xhr && errorDetail.xhr.responseText) {
        errorDiv.innerHTML = errorDetail.xhr.responseText;
      } else {
        errorDiv.textContent = 'An unknown error occurred.';
      }
      errorDiv.style.display = 'block'; // Make it visible
    }
  });
});
