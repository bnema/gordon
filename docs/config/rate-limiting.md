# Rate Limiting

Protect your Gordon instance from abuse with configurable rate limiting.

## Overview

Rate limiting prevents:
- **Brute force attacks** on authentication endpoints
- **Denial of Service (DoS)** from excessive requests
- **Resource exhaustion** from misbehaving clients
- **Registry abuse** (e.g., automated scraping)

Rate limiting applies to:
- **Registry API** (`/v2/*`) — Global + per-IP limits
- **Auth endpoints** (`/auth/*`) — Same limiter as registry (Global + per-IP)
- **Admin API** (`/admin/*`) — Global + per-IP limits (separate limiter instances)

## Quick Start

Rate limiting is **enabled by default** with sensible defaults:

```toml
[api.rate_limit]
enabled = true
global_rps = 500
per_ip_rps = 50
burst = 100
trusted_proxies = []
```

> **Important:** Gordon does not handle TLS termination, so production deployments require a front proxy (Cloudflare, nginx, etc.) for HTTPS. You **must** configure `trusted_proxies` to ensure rate limiting sees real client IPs instead of your proxy's IP.

## Configuration

```toml
[api.rate_limit]
enabled = true
global_rps = 500
per_ip_rps = 50
burst = 100
trusted_proxies = ["127.0.0.1", "10.0.0.0/8"]
```

## Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `true` | Enable or disable rate limiting |
| `global_rps` | float | `500` | Maximum requests per second across all clients combined |
| `per_ip_rps` | float | `50` | Maximum requests per second per client IP address |
| `burst` | int | `100` | Maximum burst size (requests allowed to exceed the rate temporarily) |
| `trusted_proxies` | []string | `[]` | IP addresses or CIDR ranges trusted to set `X-Forwarded-For` |

## Architecture

Gordon uses two separate rate limiters:

```
┌─────────────────────────────────────────────────────────┐
│                     Incoming Request                     │
└─────────────────────────────────────────────────────────┘
                            │
        ┌───────────────────┼───────────────────┐
        ▼                   ▼                   ▼
┌───────────────┐   ┌───────────────┐   ┌───────────────┐
│  /v2/* routes │   │ /auth/* routes│   │ /admin/* routes│
└───────────────┘   └───────────────┘   └───────────────┘
        │                   │                   │
        ▼                   ▼                   ▼
┌─────────────────────────────────┐   ┌─────────────────────────┐
│   Registry Rate Limiter          │   │   Admin Rate Limiter    │
│  ┌───────────────────┐           │   │  ┌───────────────────┐  │
│  │   Global Limiter   │          │   │  │   Global Limiter   │  │
│  │   (global_rps)     │          │   │  │   (global_rps)     │  │
│  └───────────────────┘           │   │  └───────────────────┘  │
│  ┌───────────────────┐           │   │  ┌───────────────────┐  │
│  │  Per-IP Limiters   │          │   │  │  Per-IP Limiters   │  │
│  │   (per_ip_rps)     │          │   │  │   (per_ip_rps)     │  │
│  └───────────────────┘           │   │  └───────────────────┘  │
└─────────────────────────────────┘   └─────────────────────────┘
```

**Registry Limiter** (for `/v2/*` and `/auth/*`):
- Checks global limit first (all clients combined)
- Then checks per-IP limit
- Both must pass for the request to proceed
- Auth endpoints share this limiter to prevent brute force attacks

**Admin Limiter** (for `/admin/*`):
- Separate global + per-IP limiters (independent instances)
- Isolates admin traffic from registry traffic
- Prevents CI/CD bursts from affecting admin access

## Option Details

### `enabled`

Master switch for rate limiting. When `false`, all requests are allowed without throttling.

```toml
[api.rate_limit]
enabled = false  # Disable rate limiting (not recommended for production)
```

### `global_rps`

The maximum number of requests per second that Gordon will accept from **all clients combined**. This is a hard cap that protects against distributed attacks where many IPs each send moderate traffic.

```toml
[api.rate_limit]
global_rps = 500  # 500 requests/second total
```

**Sizing guidance:**
- **Small deployments** (1-10 apps): `100-500` RPS
- **Medium deployments** (10-50 apps): `500-2000` RPS
- **Large deployments** (50+ apps): `2000-10000` RPS

Consider your CI/CD patterns—parallel builds pushing multiple images can generate bursts of traffic.

### `per_ip_rps`

The maximum requests per second allowed from a **single client IP**. This prevents any single client from monopolizing Gordon's resources.

```toml
[api.rate_limit]
per_ip_rps = 50  # 50 requests/second per IP
```

**Sizing guidance:**
- Normal Docker operations (pull, push) rarely exceed 10-20 RPS
- CI/CD pipelines with parallel jobs may need 50-100 RPS
- Automated tooling (vulnerability scanners, etc.) may need higher limits

### `burst`

The token bucket burst size. Allows clients to temporarily exceed the rate limit for short bursts, which is useful for legitimate traffic patterns like:
- Initial connection setup (multiple manifest/blob requests)
- Parallel layer downloads
- Health check bursts

```toml
[api.rate_limit]
burst = 100  # Allow bursts of up to 100 requests
```

A burst of `100` with `per_ip_rps = 50` means a client can send 100 requests instantly, then must wait ~2 seconds for the bucket to refill before another burst.

### `trusted_proxies`

**Critical for correct IP detection in production.**

Gordon is a reverse proxy that routes requests to your containers, but it **does not handle TLS termination**. For HTTPS support, you must place Gordon behind a TLS-terminating proxy like Cloudflare, nginx, or a load balancer.

```
Internet → [Cloudflare/nginx] → Gordon → Containers
              (HTTPS)          (HTTP)
```

In this setup, Gordon sees all connections coming from your front proxy's IP, not the real clients. The proxy sets `X-Forwarded-For` to communicate the original client IP.

**The problem:** `X-Forwarded-For` can be spoofed. If Gordon trusted this header unconditionally, attackers could:
- Bypass per-IP rate limits by sending fake IPs
- Create unlimited rate limiter entries (memory exhaustion)

**The solution:** Gordon only honors `X-Forwarded-For` from IPs listed in `trusted_proxies`. For all other connections, it uses the direct connection IP.

```toml
[api.rate_limit]
trusted_proxies = ["127.0.0.1", "10.0.0.0/8"]
```

**Supported formats:**
- Single IP: `"192.168.1.1"`
- CIDR range: `"10.0.0.0/8"`
- IPv6: `"::1"`, `"fd00::/8"`

> **If `trusted_proxies` is empty or misconfigured**, all requests appear to come from your proxy's IP, making per-IP rate limiting ineffective (all clients share one limit).

## Deployment Examples

### Cloudflare (Recommended)

Cloudflare provides free TLS termination and DDoS protection. This is the recommended setup for production.

Cloudflare publishes their IP ranges at https://www.cloudflare.com/ips/

```toml
[api.rate_limit]
# Cloudflare IPs (verified 2026-01-18 - check cloudflare.com/ips for updates)
trusted_proxies = [
    # IPv4
    "173.245.48.0/20",
    "103.21.244.0/22",
    "103.22.200.0/22",
    "103.31.4.0/22",
    "141.101.64.0/18",
    "108.162.192.0/18",
    "190.93.240.0/20",
    "188.114.96.0/20",
    "197.234.240.0/22",
    "198.41.128.0/17",
    "162.158.0.0/15",
    "104.16.0.0/13",
    "104.24.0.0/14",
    "172.64.0.0/13",
    "131.0.72.0/22",
    # IPv6
    "2400:cb00::/32",
    "2606:4700::/32",
    "2803:f800::/32",
    "2405:b500::/32",
    "2405:8100::/32",
    "2a06:98c0::/29",
    "2c0f:f248::/32",
]
```

> **Note:** Cloudflare IPs may change. Verify at https://www.cloudflare.com/ips/ periodically.

### Local Development (No TLS)

For local development only, Gordon can be accessed directly without a proxy:

```toml
[api.rate_limit]
trusted_proxies = []  # Empty = use RemoteAddr directly
```

> **Warning:** Never expose Gordon directly to the internet without TLS. Use this configuration only for local development.

### Behind nginx (Same Host)

When nginx runs on the same machine as Gordon:

```toml
[api.rate_limit]
trusted_proxies = ["127.0.0.1", "::1"]
```

### Behind nginx (Separate Host)

When nginx runs on a different server:

```toml
[api.rate_limit]
trusted_proxies = ["10.0.1.5"]  # nginx server IP
```

### Behind AWS Application Load Balancer (ALB)

ALB terminates TLS and forwards requests to your instances. Unlike Cloudflare, ALB IPs are dynamic and change as AWS scales the load balancer. You need to trust your VPC's private IP range (CIDR) since ALB connects from within your VPC.

```toml
[api.rate_limit]
trusted_proxies = ["10.0.0.0/16"]  # Your VPC CIDR
```

For detailed setup instructions, see the [AWS ALB Guide](../../wiki/guides/aws-alb.md).

### Behind Kubernetes Ingress

When running Gordon in Kubernetes with an ingress controller (nginx-ingress, Traefik, etc.), you need to trust the pod network CIDR. The ingress controller must also be configured to forward client IPs via `X-Forwarded-For`.

```toml
[api.rate_limit]
trusted_proxies = ["10.244.0.0/16"]  # Your pod network CIDR
```

For detailed setup instructions, see the [Kubernetes Ingress Guide](../../wiki/guides/kubernetes-ingress.md).

## Rate Limit Response

When a client exceeds the rate limit, Gordon returns:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Docker-Distribution-API-Version: registry/2.0
Retry-After: 1

{
  "errors": [
    {
      "code": "TOOMANYREQUESTS",
      "message": "rate limit exceeded"
    }
  ]
}
```

The `Retry-After` header indicates when the client can retry (in seconds).

## Monitoring

Rate-limited requests are logged at `info` level:

```
level=info msg="rate limit exceeded" client_ip=203.0.113.50 path=/v2/myapp/manifests/latest
```

To see rate limiting in action, enable debug logging:

```toml
[logging]
level = "debug"
```

## Security Considerations

### IP Spoofing Prevention

If `trusted_proxies` is misconfigured, attackers can:
1. Send requests with fake `X-Forwarded-For: 1.2.3.4`
2. Each request appears to come from a different IP
3. Per-IP rate limiting becomes ineffective
4. Attackers create unbounded limiter entries (memory exhaustion)

**Always verify your proxy configuration** by checking logs to ensure client IPs are detected correctly.

### Endpoint Coverage

All authenticated endpoints are protected:

| Endpoint | Rate Limiter | Limits Applied |
|----------|--------------|----------------|
| `/v2/*` (registry) | Registry limiter | Global RPS + Per-IP RPS |
| `/auth/*` (authentication) | Registry limiter | Global RPS + Per-IP RPS |
| `/admin/*` (management) | Admin limiter | Global RPS + Per-IP RPS |

The auth endpoints share the registry limiter, preventing brute force attacks on credentials. The admin API uses separate limiter instances to avoid CI/CD traffic affecting admin access.

### Recommended Production Settings

```toml
[api.rate_limit]
enabled = true
global_rps = 1000      # Adjust based on your traffic
per_ip_rps = 30        # Stricter per-IP limit
burst = 50             # Smaller burst for tighter control
trusted_proxies = []   # Configure based on your proxy setup
```

## Troubleshooting

### "I'm getting rate limited but traffic is low"

1. Check if `trusted_proxies` is configured correctly
2. All traffic may appear to come from your proxy's IP
3. Add your proxy to `trusted_proxies` to see real client IPs

### "Rate limiting doesn't seem to work"

1. Verify `enabled = true`
2. Check that requests go through Gordon (not cached by CDN)
3. Ensure `global_rps` and `per_ip_rps` are set appropriately

### "I need to whitelist certain IPs"

Rate limiting doesn't support IP whitelisting. Consider:
- Increasing `per_ip_rps` for legitimate high-traffic clients
- Using a reverse proxy with its own rate limiting rules
- Implementing IP-based access control at the network level

## Related

- [Authentication](./auth.md)
- [Configuration Reference](./reference.md)
- [Logging](./logging.md)
