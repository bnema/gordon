# Troubleshooting

Common issues and solutions when using Gordon.

## Registry Issues

### "unauthorized: authentication required"

**Cause:** Registry authentication is enabled but credentials are missing or invalid.

**Solutions:**

1. Login to registry:
   ```bash
   docker login registry.mydomain.com
   ```

2. Check token is valid:
   ```bash
   gordon auth token list
   ```

3. Generate new token:
   ```bash
   gordon auth token generate --subject myuser --expiry 0
   ```

### "connection refused" on push

**Cause:** Gordon is not running or registry port is blocked.

**Solutions:**

1. Check Gordon is running:
   ```bash
   systemctl --user status gordon
   # or
   pgrep -f "gordon start"
   ```

2. Check registry port is accessible:
   ```bash
   curl -v http://localhost:5000/v2/
   ```

3. Check firewall:
   ```bash
   sudo firewall-cmd --list-ports
   ```

### "unknown: image not found"

**Cause:** Image was pushed but no route configured.

**Solution:** Add route to config:
```toml
[routes]
"app.mydomain.com" = "myapp:latest"
```

Then reload:
```bash
gordon reload
```

## Deployment Issues

### Container starts but app not accessible

**Causes and solutions:**

1. **Wrong port exposed:**
   Check your Dockerfile exposes the correct port:
   ```dockerfile
   EXPOSE 3000
   ```

2. **Multiple ports - wrong one selected:**
   Add `gordon.proxy.port` label:
   ```dockerfile
   LABEL gordon.proxy.port=3000
   ```

3. **App not listening on 0.0.0.0:**
   Ensure app binds to `0.0.0.0`, not `127.0.0.1`:
   ```javascript
   app.listen(3000, '0.0.0.0');
   ```

### Container keeps restarting

**Cause:** Application crashing on startup.

**Solutions:**

1. Check container logs:
   ```bash
   docker logs gordon-app-mydomain-com
   ```

2. Check Gordon logs:
   ```bash
   gordon logs -f
   ```

3. Run container manually to debug:
   ```bash
   docker run -it registry.mydomain.com/myapp:latest sh
   ```

### Environment variables not loaded

**Cause:** Env file not found or wrong format.

**Solutions:**

1. Check file exists with correct name:
   ```bash
   ls ~/.gordon/env/
   # Should show: app_mydomain_com.env (dots → underscores)
   ```

2. Check file permissions:
   ```bash
   chmod 600 ~/.gordon/env/app_mydomain_com.env
   ```

3. Check file format (no spaces around `=`):
   ```bash
   # Correct
   KEY=value

   # Wrong
   KEY = value
   ```

### Secrets not resolved

**Cause:** Secret provider not available or secret not found.

**Solutions:**

1. For `pass` backend:
   ```bash
   # Check pass is available
   which pass

   # Check secret exists
   pass show myapp/db-password
   ```

2. For `sops` backend:
   ```bash
   # Check sops is available
   which sops

   # Check file can be decrypted
   sops -d secrets.yaml
   ```

## Network Issues

### Containers can't reach each other

**Cause:** Network isolation enabled but services not in same network.

**Solutions:**

1. Check attachments are configured:
   ```toml
   [attachments]
   "app.mydomain.com" = ["postgres:latest"]
   ```

2. Check containers are in same network:
   ```bash
   docker network inspect gordon-app-mydomain-com
   ```

3. Use correct hostname (image name before colon):
   ```javascript
   // Correct
   connect("postgresql://postgres:5432/mydb")

   // Wrong
   connect("postgresql://localhost:5432/mydb")
   ```

### DNS resolution failing

**Cause:** Container can't resolve service names.

**Solutions:**

1. Verify network isolation is enabled:
   ```toml
   [network_isolation]
   enabled = true
   ```

2. Check both containers in same network:
   ```bash
   docker network inspect gordon-app-mydomain-com
   ```

## Volume Issues

### Data not persisting

**Cause:** Volume not configured or not preserved.

**Solutions:**

1. Add VOLUME to Dockerfile:
   ```dockerfile
   VOLUME ["/data"]
   ```

2. Check volume preservation:
   ```toml
   [volumes]
   preserve = true  # default: true
   ```

3. List volumes:
   ```bash
   docker volume ls | grep gordon
   ```

### Disk full

**Cause:** Registry or logs consuming too much space.

**Solutions:**

1. Check disk usage:
   ```bash
   du -sh ~/.gordon/*
   ```

2. Prune unused images:
   ```bash
   docker image prune -a
   ```

3. Configure log rotation:
   ```toml
   [logging.file]
   max_size = 100
   max_backups = 3
   ```

## Configuration Issues

### Config not reloading

**Cause:** Gordon not watching config file.

**Solution:** Manual reload:
```bash
gordon reload
```

### "failed to read config file"

**Cause:** TOML syntax error.

**Solution:** Validate TOML:
```bash
# Check for syntax errors
cat ~/.config/gordon/gordon.toml | tomlv
# or use online validator
```

## Logging Issues

### No logs appearing

**Cause:** File logging not enabled.

**Solution:** Enable in config:
```toml
[logging.file]
enabled = true
path = "~/.gordon/logs/gordon.log"
```

### Container logs missing

**Cause:** Container log collection disabled.

**Solution:**
```toml
[logging.container_logs]
enabled = true  # default: true
```

## Diagnostic Commands

```bash
# Check Gordon status
systemctl --user status gordon

# View Gordon logs
gordon logs -f
journalctl --user -u gordon -f

# List containers
docker ps -f "label=gordon.managed=true"

# List networks
docker network ls | grep gordon

# List volumes
docker volume ls | grep gordon

# Check container logs
docker logs gordon-app-mydomain-com

# Inspect container
docker inspect gordon-app-mydomain-com

# Check connectivity
curl -v http://localhost:5000/v2/
curl -v http://localhost:8080/
```

## Getting Help

- [GitHub Issues](https://github.com/bnema/gordon/issues)
- [GitHub Discussions](https://github.com/bnema/gordon/discussions)

## Related

- [Installation](../installation.md)
- [Configuration](../config/index.md)
- [CLI Reference](../cli/index.md)
