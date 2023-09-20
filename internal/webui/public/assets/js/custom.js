document.body.addEventListener('htmx:afterRequest', function (evt) {
  const targetError = evt.target.attributes.getNamedItem('hx-target-error');
  if (evt.detail.failed && targetError) {
    const errorElement = document.getElementById(targetError.value);
    errorElement.innerHTML = evt.detail.xhr.responseText; // Set the response text as content
    errorElement.style.display = "inline";
  }
});

document.body.addEventListener('htmx:beforeRequest', function (evt) {
  const targetError = evt.target.attributes.getNamedItem('hx-target-error');
  if (targetError) {
    document.getElementById(targetError.value).style.display = "none";
  }
});
