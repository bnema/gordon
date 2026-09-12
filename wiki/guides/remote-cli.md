# Remote CLI

Gordon management commands use the authenticated daemon API. The default local transport is HTTP over the owner-only Unix socket; a configured remote uses authenticated HTTP/TLS.

## Configure a remote

Store the server URL and credentials with Gordon's remote configuration commands. Avoid placing tokens directly in shell history, scripts, or repository files.

Verify connectivity:

```bash
gordon status --remote https://gordon.example.com --token "$GORDON_TOKEN"
```

## App management

The app name is the canonical workload identity. Domains are routing addresses declared by the manifest.

```bash
gordon apps apply --file app.toml --remote https://gordon.example.com --token "$GORDON_TOKEN"
gordon apps list --remote https://gordon.example.com --token "$GORDON_TOKEN"
gordon apps show APP --remote https://gordon.example.com --token "$GORDON_TOKEN"
gordon apps deploy APP --remote https://gordon.example.com --token "$GORDON_TOKEN"
gordon apps logs APP --service SERVICE --remote https://gordon.example.com --token "$GORDON_TOKEN"
```

Lifecycle commands use the same transport:

```bash
gordon apps stop APP
gordon apps start APP
gordon apps restart APP --service SERVICE
gordon apps remove APP
```

## Backups

Backup targets come from the ACTIVE app manifest and use app, service, and resource identity:

```bash
gordon backups run APP --service SERVICE --database DATABASE
gordon backups volume run APP --service SERVICE --volume VOLUME
gordon backups status
gordon backups volume status
```

## Logs

`gordon logs` streams daemon process logs. Workload logs use the app command:

```bash
gordon logs --remote https://gordon.example.com --token "$GORDON_TOKEN"
gordon apps logs APP --service SERVICE --remote https://gordon.example.com --token "$GORDON_TOKEN"
```

## Security

- Use HTTPS for remote access.
- Scope and rotate tokens.
- Keep the local admin socket owner-only.
- Never expose the local socket through a public proxy.
- Prefer environment variables or a protected credential store over literal tokens.
