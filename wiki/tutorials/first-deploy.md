# Deploy Your First App

Deploy a simple web application to Gordon in under 10 minutes.

## What You'll Learn

- Building a container image
- Pushing to Gordon's registry
- Configuring routes
- Deploying your app

## Prerequisites

- Gordon running on your VPS
- Docker installed locally
- Domain configured (e.g., `app.mydomain.com`)

## Steps

### 1. Create a Simple App

Create a new directory with a simple Node.js app:

```bash
mkdir my-first-app
cd my-first-app
```

Create `server.js`:

```javascript
const http = require('http');

const port = process.env.PORT || 3000;

const server = http.createServer((req, res) => {
  res.writeHead(200, { 'Content-Type': 'text/html' });
  res.end(`
    <html>
      <body>
        <h1>Hello from Gordon!</h1>
        <p>Deployed at: ${new Date().toISOString()}</p>
      </body>
    </html>
  `);
});

server.listen(port, '0.0.0.0', () => {
  console.log(`Server running on port ${port}`);
});
```

Create `Dockerfile`:

```dockerfile
FROM node:18-alpine
WORKDIR /app
COPY server.js .
ENV PORT=3000
EXPOSE 3000
CMD ["node", "server.js"]
```

### 2. Build the Image

```bash
docker build -t my-first-app .
```

### 3. Tag for Gordon Registry

Replace `registry.mydomain.com` with your Gordon registry domain:

```bash
docker tag my-first-app registry.mydomain.com/my-first-app:latest
```

### 4. Configure the Route

On your Gordon server, edit `~/.config/gordon/gordon.toml`:

```toml
[routes]
"app.mydomain.com" = "my-first-app:latest"
```

Reload Gordon:

```bash
gordon reload
```

### 5. Push to Deploy

Login to your registry (if auth enabled):

```bash
docker login registry.mydomain.com
```

Push the image:

```bash
docker push registry.mydomain.com/my-first-app:latest
```

### 6. Verify Deployment

Visit `https://app.mydomain.com` in your browser.

You should see "Hello from Gordon!" with the deployment timestamp.

## Common Issues

### "unauthorized" on push

Enable registry authentication or check credentials:

```bash
gordon auth token generate --subject deploy --expiry 0
docker login -u deploy -p <token> registry.mydomain.com
```

### App not accessible

1. Check the container is running:
   ```bash
   docker ps | grep my-first-app
   ```

2. Check Gordon logs:
   ```bash
   gordon logs -f
   ```

3. Ensure DNS points to your server

### Wrong port

If your app uses a different port, expose it in Dockerfile:

```dockerfile
ENV PORT=8080
EXPOSE 8080
```

## Updating Your App

Make changes, then rebuild and push:

```bash
# Edit server.js
docker build -t my-first-app .
docker tag my-first-app registry.mydomain.com/my-first-app:latest
docker push registry.mydomain.com/my-first-app:latest
```

Gordon automatically deploys the update with zero downtime.

## Next Steps

- [Add environment variables](/docs/config/env.md)
- [Add a database](./postgres-service.md)
- [Set up CI/CD](/docs/deployment/github-actions.md)
