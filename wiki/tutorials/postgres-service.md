# Add PostgreSQL to Your App

Add a PostgreSQL database as an attachment to your application.

## What You'll Learn

- Creating a custom PostgreSQL image with persistence
- Configuring attachments
- Connecting your app to the database

## Prerequisites

- Gordon running with an app deployed
- Basic understanding of [routes](/docs/config/routes.md) and [attachments](/docs/config/attachments.md)

## Steps

### 1. Create a Custom PostgreSQL Image

Create `postgres.Dockerfile`:

```dockerfile
FROM postgres:15-alpine

# Persistent data directory
VOLUME ["/var/lib/postgresql/data"]

# Default configuration (override via env file)
ENV POSTGRES_DB=myapp
ENV POSTGRES_USER=myapp
```

### 2. Build and Push PostgreSQL Image

```bash
docker build -f postgres.Dockerfile -t my-postgres .
docker tag my-postgres registry.mydomain.com/my-postgres:latest
docker push registry.mydomain.com/my-postgres:latest
```

### 3. Configure the Attachment

Edit `~/.config/gordon/gordon.toml`:

```toml
[routes]
"app.mydomain.com" = "my-app:latest"

[network_isolation]
enabled = true

[attachments]
"app.mydomain.com" = ["my-postgres:latest"]
```

Reload Gordon:

```bash
gordon reload
```

### 4. Configure Database Password

Create an env file for your app at `~/.gordon/env/app_mydomain_com.env`:

```bash
# Application settings
NODE_ENV=production
PORT=3000

# Database connection
DATABASE_HOST=my-postgres
DATABASE_PORT=5432
DATABASE_NAME=myapp
DATABASE_USER=myapp
DATABASE_PASSWORD=your-secure-password

# Full connection URL
DATABASE_URL=postgresql://myapp:your-secure-password@my-postgres:5432/myapp
```

Create an env file for PostgreSQL at `~/.gordon/env/my-postgres.env`:

```bash
POSTGRES_PASSWORD=your-secure-password
```

### 5. Update Your Application

Connect to the database using the service name:

```javascript
// Node.js with pg
const { Pool } = require('pg');

const pool = new Pool({
  host: process.env.DATABASE_HOST || 'my-postgres',
  port: process.env.DATABASE_PORT || 5432,
  database: process.env.DATABASE_NAME || 'myapp',
  user: process.env.DATABASE_USER || 'myapp',
  password: process.env.DATABASE_PASSWORD,
});
```

```python
# Python with psycopg2
import os
import psycopg2

conn = psycopg2.connect(
    host=os.environ.get('DATABASE_HOST', 'my-postgres'),
    port=os.environ.get('DATABASE_PORT', 5432),
    database=os.environ.get('DATABASE_NAME', 'myapp'),
    user=os.environ.get('DATABASE_USER', 'myapp'),
    password=os.environ['DATABASE_PASSWORD'],
)
```

### 6. Rebuild and Deploy

```bash
docker build -t my-app .
docker tag my-app registry.mydomain.com/my-app:latest
docker push registry.mydomain.com/my-app:latest
```

## Verify Connection

### Check Containers

```bash
docker ps | grep gordon-app-mydomain
```

You should see both your app and postgres containers.

### Check Network

```bash
docker network inspect gordon-app-mydomain-com
```

Both containers should be in the same network.

### Test Connection

SSH to your server and test:

```bash
docker exec -it gordon-app-mydomain-com-my-postgres psql -U myapp -d myapp
```

## Using Secrets

For production, use a secrets backend instead of plain text:

```bash
# Store password in pass
pass insert myapp/db-password
```

Update env file:

```bash
DATABASE_PASSWORD=${pass:myapp/db-password}
POSTGRES_PASSWORD=${pass:myapp/db-password}
```

## Data Persistence

PostgreSQL data persists in a Docker volume:

```bash
# List volumes
docker volume ls | grep postgres

# Volume name: gordon-app-mydomain-com-my-postgres-var-lib-postgresql-data
```

Data survives:
- Container restarts
- App updates
- Gordon reloads

## Backup and Restore

### Backup

```bash
docker exec gordon-app-mydomain-com-my-postgres \
  pg_dump -U myapp myapp > backup.sql
```

### Restore

```bash
cat backup.sql | docker exec -i gordon-app-mydomain-com-my-postgres \
  psql -U myapp myapp
```

## Next Steps

- [Add Redis cache](./multi-service-app.md)
- [Configure secrets](/docs/config/secrets.md)
- [Network isolation](/docs/config/network-isolation.md)
