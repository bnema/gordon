# AI Agent Deployment Guide

Use Gordon's declarative app workflow. Do not edit daemon state or infer workload identity from domains.

## Required files

```text
~/.config/gordon/gordon.toml  # Daemon configuration
app.toml                      # Declarative app manifest
```

## Workflow

1. Define services, routes, networks, volumes, secrets, readiness, and backups in `app.toml`.
2. Validate and persist the manifest:

   ```bash
   gordon apps apply --file app.toml
   ```

3. Register required secret values without placing them in the manifest:

   ```bash
   gordon apps secrets set APP --service SERVICE KEY
   ```

4. Deploy the accepted revision:

   ```bash
   gordon apps deploy APP
   ```

5. Verify state and workload logs:

   ```bash
   gordon apps show APP
   gordon apps logs APP --service SERVICE
   ```

The local CLI uses the authenticated owner-only Unix socket. To target another Gordon instance, use the configured authenticated HTTP/TLS remote transport.

## Registry authentication

Generate a scoped token on the server, then pass it to the registry client through standard input. Never place tokens in manifests, command history, fixtures, or logs.

## Safety rules

- Use app and service names as workload identity; domains are routing addresses only.
- Keep secret values outside manifests and app state.
- Reuse the same idempotency key only to inspect or retry the same mutation request.
- Do not delete retained volumes or secrets during app removal.
