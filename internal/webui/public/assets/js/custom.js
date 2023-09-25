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

