# Health Monitoring with systemd

Monitor your Gordon routes and get desktop notifications when something goes down. Uses `gordon routes list --json`, a bash script, and a systemd user timer.

## Prerequisites

- Gordon CLI installed
- `jq` and `notify-send` (libnotify) available
- systemd user session (most Linux desktops)

For remote instances, configure a saved remote with `gordon remotes add` and `gordon remotes use` (see [Remote CLI Management](./remote-cli.md)). The CLI uses the active remote automatically.

---

## The Check Script

Create `~/.local/bin/gordon-watchdog.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

GORDON_CMD="gordon routes list --json"
# For remote monitoring, configure a saved remote first:
#   gordon remotes add myserver https://gordon.example.com --token-env GORDON_TOKEN
#   gordon remotes use myserver
# The CLI then targets the remote automatically.

json=$($GORDON_CMD) || {
    notify-send -u critical "Gordon Watchdog" "Cannot reach Gordon"
    exit 1
}

problems=()

while IFS= read -r route; do
    domain=$(echo "$route" | jq -r '.domain')
    http_status=$(echo "$route" | jq -r '.http_status')
    container_status=$(echo "$route" | jq -r '.container_status')

    # HTTP status: flag 4xx, 5xx, 0 (no response), or unavailable
    if ! [[ "$http_status" =~ ^[0-9]+$ ]]; then
        problems+=("$domain: HTTP status unavailable ($http_status)")
    elif [[ "$http_status" -eq 0 ]] || [[ "$http_status" -ge 400 ]]; then
        problems+=("$domain: HTTP $http_status")
    fi

    # Container not running
    if [[ "$container_status" != "running" ]]; then
        problems+=("$domain: container $container_status")
    fi

    # Check attachments (databases, sidecars)
    while IFS= read -r att; do
        att_name=$(echo "$att" | jq -r '.name')
        att_status=$(echo "$att" | jq -r '.status')
        if [[ "$att_status" != "running" ]]; then
            problems+=("$domain/$att_name: $att_status")
        fi
    done < <(echo "$route" | jq -c '.attachments[]? // empty')

done < <(echo "$json" | jq -c '.[]')

if [[ ${#problems[@]} -gt 0 ]]; then
    body=$(printf '%s\n' "${problems[@]}")
    notify-send -u critical -i dialog-warning \
        "Gordon Watchdog" \
        "${#problems[@]} problem(s):\n$body"
    echo "[$(date -Iseconds)] ALERT: ${#problems[@]} problem(s)"
    printf '  %s\n' "${problems[@]}"
else
    echo "[$(date -Iseconds)] OK: all routes healthy"
fi
```

Make it executable:

```bash
chmod +x ~/.local/bin/gordon-watchdog.sh
```

Test it:

```bash
~/.local/bin/gordon-watchdog.sh
# Expected: [YYYY-MM-DDThh:mm:ss+00:00] OK: all routes healthy
```

## What Gets Flagged

| Condition | Example |
|-----------|---------|
| HTTP 4xx or 5xx | App returning 500 |
| HTTP 0 | Container not responding at all |
| Container not running | Crashed or stopped container |
| Attachment not running | Database sidecar down |

Redirects (301, 302, 303, 307) are not flagged. These are normal for apps that redirect HTTP to HTTPS or to a login page.

## systemd Timer

Two unit files go in `~/.config/systemd/user/`.

### Service unit

`gordon-watchdog.service`:

```ini
[Unit]
Description=Gordon Routes Health Check
After=network-online.target

[Service]
Type=oneshot
ExecStart=%h/.local/bin/gordon-watchdog.sh
Environment=DISPLAY=:0
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%U/bus
```

### Timer unit

`gordon-watchdog.timer`:

```ini
[Unit]
Description=Run Gordon Watchdog every 5 minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=5min
Persistent=true

[Install]
WantedBy=timers.target
```

### Enable it

```bash
systemctl --user daemon-reload
systemctl --user enable --now gordon-watchdog.timer
```

### Verify

```bash
# Timer is active
systemctl --user status gordon-watchdog.timer

# Run it manually
systemctl --user start gordon-watchdog.service

# Check logs
journalctl --user -u gordon-watchdog.service --since "1 hour ago"
```

---

## Customization

**Change the interval**: edit `OnUnitActiveSec` in the timer file. Use `1min`, `10min`, `30min`, etc.

**Log to a file**: add output redirection in the service unit:

```ini
StandardOutput=append:%h/.local/share/gordon-watchdog.log
StandardError=append:%h/.local/share/gordon-watchdog.log
```

**Send alerts elsewhere**: replace `notify-send` in the script with whatever you need. `curl` a webhook, write to a Slack channel, send an email. The `$body` variable has the list of problems, one per line.

**Stricter HTTP checks**: if you want to flag anything that isn't exactly 200, replace the HTTP check with:

```bash
if ! [[ "$http_status" =~ ^[0-9]+$ ]]; then
    problems+=("$domain: HTTP status unavailable ($http_status)")
elif [[ "$http_status" -ne 200 ]]; then
    problems+=("$domain: HTTP $http_status")
fi
```

**Remote monitoring**: configure a saved remote with `gordon remotes add` and `gordon remotes use`. The script picks it up automatically from `~/.config/gordon/remotes.toml`.

---

## Troubleshooting

### No desktop notification

The systemd user service needs access to the D-Bus session bus and display. If notifications don't appear:

```bash
# Check your UID
id -u
# Should match the %U in the service file (usually 1000)

# Verify D-Bus socket exists
ls /run/user/$(id -u)/bus
```

On Wayland, you may also need `WAYLAND_DISPLAY` in the service environment.

### "Cannot reach Gordon" on every run

If Gordon runs as a user service, ensure it's started before the watchdog. Add to the service unit:

```ini
[Unit]
After=network-online.target gordon.service
```

### Timer not firing

```bash
systemctl --user list-timers --all | grep gordon
```

If the timer shows `n/a` for next run, re-enable it:

```bash
systemctl --user reenable gordon-watchdog.timer
systemctl --user start gordon-watchdog.timer
```
