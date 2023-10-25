package cmd

// the push command is used to push a container image to gordon's endpoint

// Pseudo code of the steps for the push command

// 1. Extract the container image as .tar and store it in a temporary directory

// 2. Prepare a payload with the .tar as a byte array, the image name and tag (if any)

// 3. append the payload to the request body with the type "push" and the token

// 4. Send the request to the backend

// 5. If the response is 200, print the success message
