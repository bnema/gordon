# Gordon Security Audit Report

**Date**: 2026-02-01
**Scope**: Full security review with focus on reverse proxy, Docker socket access, secrets management, authentication/authorization
**Severity scale**: CRITICAL > HIGH > MEDIUM > LOW > INFO
**Status**: Fixes applied for C1, C2, H1-H5, M2, M3, M5, M6, L2

---

## Executive Summary

Gordon is a well-structured project following hexagonal architecture with clear separation of concerns. The codebase shows strong security awareness in many areas (SSRF protection, constant-time comparisons, bcrypt, path traversal prevention, file permissions). However, several vulnerabilities were identified, primarily around the reverse proxy layer, header trust boundaries, credential exposure, and container isolation.

**Total findings**: 18
- CRITICAL: 2
- HIGH: 5
- MEDIUM: 6
- LOW: 3
- INFO: 2

---

## CRITICAL

### C1 — Credential Logging in Plaintext (CWE-532)

**File**: `internal/adapters/out/docker/runtime.go:408-411`

```go
log.Debug().
    Str("server_address", serverAddress).
    Str("auth_json", string(authConfigBytes)).
    Msg("auth config for pull")
```

The `PullImageWithAuth` function logs the full authentication JSON (containing `username`, `password`, `server_address`) at DEBUG level. If debug logging is enabled in production (or logs are shipped to a centralized system), credentials for the Docker registry are exposed in plaintext in log files.

**Impact**: Any user or system with read access to Gordon's log files obtains registry credentials. This enables unauthorized image push/pull, potentially injecting malicious images that Gordon will auto-deploy.

**Recommendation**: Remove the `auth_json` field entirely from the log statement. Log only the `server_address` and a boolean indicating auth is present.

---

### C2 — Container Ports Bound to 0.0.0.0 (CWE-668)

**File**: `internal/adapters/out/docker/runtime.go:73-78`

```go
portBindings[containerPort] = []nat.PortBinding{
    {
        HostIP:   "0.0.0.0",
        HostPort: "0", // Docker will assign a random available port
    },
}
```

All container ports are bound to `0.0.0.0` (all network interfaces). On a server with a public IP, this means every deployed container is directly accessible from the internet on its random host port, **completely bypassing Gordon's reverse proxy, authentication, rate limiting, and security headers**.

**Impact**: An attacker can port-scan the server, find the random host ports, and access containers directly without going through Gordon's proxy. This bypasses all security layers. If a container has vulnerabilities or exposes admin interfaces, they are directly reachable.

**Recommendation**: Bind to `127.0.0.1` instead of `0.0.0.0`. When Gordon runs in a container, use Docker network communication (which is already implemented) and don't publish ports at all.

---

## HIGH

### H1 — WWW-Authenticate Realm Injection via X-Forwarded-Host (CWE-113)

**File**: `internal/adapters/in/http/middleware/auth.go:421-434`

```go
realmHost := r.Header.Get("X-Forwarded-Host")
if realmHost == "" {
    realmHost = host
}
scheme := "http"
if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
    scheme = proto
}
realm := scheme + "://" + realmHost + "/auth/token"
w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="gordon-registry"`)
```

The `sendUnauthorized` function constructs the `WWW-Authenticate` realm URL using `X-Forwarded-Host` and `X-Forwarded-Proto` headers from the incoming request **without any validation or trusted proxy check**. An attacker can:

1. Set `X-Forwarded-Host: evil.com` to redirect Docker clients to a phishing server during the auth challenge flow
2. Inject header content via `X-Forwarded-Host: evil.com", fake="` to manipulate the WWW-Authenticate header
3. Set `X-Forwarded-Proto: javascript:` for potential XSS in clients that render the realm URL

**Impact**: Docker clients receiving the 401 response will be directed to the attacker's server for authentication, leaking registry credentials.

**Recommendation**: Only trust `X-Forwarded-Host` and `X-Forwarded-Proto` when the request comes from a configured trusted proxy. Validate the values against expected formats.

---

### H2 — X-Forwarded-Proto Spoofing on Proxy (CWE-346)

**File**: `internal/usecase/proxy/service.go:291-297`

```go
origProto := pr.In.Header.Get("X-Forwarded-Proto")
pr.SetXForwarded()
if origProto != "" {
    pr.Out.Header.Set("X-Forwarded-Proto", origProto)
}
```

The reverse proxy preserves the `X-Forwarded-Proto` header from any incoming request without checking if it comes from a trusted proxy. An attacker can set `X-Forwarded-Proto: https` on a direct HTTP connection, making the backend application believe the connection is over TLS.

**Impact**: Backend applications that check `X-Forwarded-Proto` for security decisions (e.g., secure cookie flags, HSTS enforcement, redirect loops) will be tricked. This can lead to mixed-content issues, cookie theft (if `Secure` flag is skipped), or HTTPS redirect bypasses.

**Recommendation**: Only preserve `X-Forwarded-Proto` when the request arrives from a trusted proxy. Otherwise, set it based on the actual connection (`r.TLS != nil`).

---

### H3 — Wildcard CORS on Reverse Proxy (CWE-942)

**File**: `internal/adapters/in/http/middleware/logging.go:149-162`

```go
func CORS(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
```

The CORS middleware applied to the proxy handler sets `Access-Control-Allow-Origin: *` on **all proxied responses**. This allows any website on the internet to make cross-origin requests to applications behind Gordon.

**Impact**: If any proxied application uses cookie-based authentication or exposes sensitive APIs, any malicious website can read responses cross-origin. This is particularly dangerous because:
- It overrides any CORS policy the backend application sets
- It applies uniformly to all routes regardless of their security requirements
- Combined with `Authorization` in allowed headers, it enables cross-origin authenticated requests

**Recommendation**: Remove the blanket CORS middleware from the proxy chain. Let backend applications control their own CORS policies. If CORS is needed at the proxy level, make it configurable per route.

---

### H4 — IP Spoofing in Request Logging (CWE-348)

**File**: `internal/adapters/in/http/middleware/logging.go:96-120`

```go
func getClientIP(r *http.Request) string {
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        // Take the first IP (original client)
        ...
        return xff
    }
    if xri := r.Header.Get("X-Real-IP"); xri != "" {
        return xri
    }
```

The `getClientIP()` function in the logging middleware unconditionally trusts `X-Forwarded-For` and `X-Real-IP` headers from any source. This is different from `GetClientIP()` in `clientip.go` which properly validates trusted proxies.

**Impact**: An attacker can forge their IP in all Gordon logs by setting `X-Forwarded-For: <any-ip>`. This:
- Makes forensic analysis unreliable
- Can be used to frame other IPs in audit logs
- Undermines any IP-based monitoring or alerting
- The per-IP rate limiting on registry/admin uses the proper `GetClientIP()`, but operational visibility is degraded

**Recommendation**: Use the trusted-proxy-aware `GetClientIP()` from `clientip.go` in the logging middleware instead of the naive `getClientIP()`.

---

### H5 — Full Admin API Exposed When Auth Disabled (CWE-306)

**File**: `internal/adapters/in/http/admin/middleware.go:54-64`

```go
if !authSvc.IsEnabled() {
    log.Warn().
        Str("path", r.URL.Path).
        Str("method", r.Method).
        Str("remote_addr", r.RemoteAddr).
        Msg("auth disabled - allowing unauthenticated admin API access")
    next.ServeHTTP(w, r)
    return
}
```

When authentication is disabled, the admin API is fully accessible without any credentials. This includes endpoints to: deploy containers (`/admin/deploy`), manage secrets (`/admin/secrets`), modify routes (`/admin/routes`), reload configuration (`/admin/reload`), and view all config including data directory paths (`/admin/config`).

**Impact**: If a user disables auth (even temporarily for debugging), anyone who can reach the registry port gains full control: they can deploy arbitrary containers, read/write secrets, and reconfigure Gordon.

**Recommendation**: When auth is disabled, either restrict admin API to localhost only, or require a separate flag (`--allow-unauthenticated-admin`) to explicitly enable it. At minimum, block write operations when auth is disabled.

---

## MEDIUM

### M1 — SSRF Protection Only on External Routes (CWE-918)

**File**: `internal/usecase/proxy/service.go:131-170`

SSRF validation (`ValidateExternalRouteTarget`) is only applied to external routes. Container routes (which resolve to container IPs) are not validated against the blocked CIDR list. While this is architecturally expected (Gordon needs to talk to containers on internal networks), there's a subtlety:

When Gordon runs on the host (not in container), it resolves targets to `localhost:<port>` (line 222-227). If a route's container is somehow compromised or an attacker can influence the container lookup (e.g., through a race condition during zero-downtime deployment), the proxy could be directed to arbitrary localhost ports.

**Recommendation**: For host-mode proxying, validate that the resolved host port actually belongs to a Gordon-managed container. Consider maintaining a whitelist of known container ports.

---

### M2 — No Request Body Size Limit on Proxy (CWE-400)

**File**: `internal/usecase/proxy/service.go` and `internal/adapters/in/http/proxy/handler.go`

The reverse proxy has no limit on request body size. While the admin API uses `http.MaxBytesReader(w, r.Body, maxAdminRequestSize)`, proxied requests pass through without any body size constraint.

**Impact**: An attacker can upload extremely large payloads through the proxy to exhaust disk space or memory on backend containers, or to conduct a slow-loris style denial of service.

**Recommendation**: Add a configurable maximum body size for proxied requests (e.g., `proxy.max_body_size = "100MB"` in config) with a reasonable default.

---

### M3 — SOPS Provider Accepts Absolute File Paths (CWE-22)

**File**: `internal/adapters/out/secrets/sops.go:91-95`

```go
cleanPath := filepath.Clean(filePath)
if strings.Contains(cleanPath, "..") {
    return "", fmt.Errorf("invalid file path: path traversal not allowed")
}
```

Unlike the pass provider which explicitly rejects absolute paths (`strings.HasPrefix(path, "/")`), the SOPS provider only checks for `..` sequences. An absolute path like `/etc/shadow` would pass validation and be passed to the `sops` command.

**Impact**: If an attacker can control the secret reference in an env file (e.g., `${sops:/etc/shadow:root}`), they could read arbitrary files on the system (assuming sops can decrypt them or the file is unencrypted).

**Recommendation**: Add absolute path rejection to the SOPS provider, matching the pass provider's behavior.

---

### M4 — Race Condition in Proxy Target Cache (CWE-362)

**File**: `internal/usecase/proxy/service.go:118-129`

```go
s.mu.RLock()
if target, exists := s.targets[domainName]; exists {
    s.mu.RUnlock()
    return target, nil
}
s.mu.RUnlock()
// ... resolve target ...
s.mu.Lock()
s.targets[domainName] = target
s.mu.Unlock()
```

There's a TOCTOU (time-of-check-time-of-use) race between the RLock read and the Lock write. Multiple concurrent requests for the same uncached domain will all proceed to resolve the target, and the last one to finish will overwrite the cache. During zero-downtime deployments, this could cause a brief window where some requests go to the old container and some to the new one.

**Impact**: During deployments, inconsistent routing behavior. Not directly exploitable for security but could cause availability issues.

**Recommendation**: Use a singleflight pattern (`golang.org/x/sync/singleflight`) for target resolution to ensure only one goroutine resolves a target for a given domain at a time.

---

### M5 — Unreliable Container Detection (CWE-697)

**File**: `internal/usecase/proxy/service.go:374-405`

```go
if hostname, err := os.Hostname(); err == nil {
    if len(hostname) == 12 || len(hostname) == 64 {
        return true
    }
}
```

The `isRunningInContainer()` function uses hostname length (12 or 64 characters) as a container detection heuristic. A host machine with a 12-character hostname (e.g., `web-server-1`) would be falsely detected as running in a container.

**Impact**: Incorrect detection changes how the proxy resolves targets — container mode uses internal Docker network IPs, while host mode uses localhost port mappings. False positive detection would cause the proxy to attempt container network communication from the host, resulting in all proxied requests failing (502/503 errors).

**Recommendation**: Make the runtime mode explicitly configurable (e.g., `server.container_mode = true/false` in config) instead of relying on heuristics. Use the heuristic only as a default when not configured.

---

### M6 — SSE Log Streaming Without Line Escaping (CWE-117)

**File**: `internal/adapters/in/http/admin/handler.go:896`

```go
_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
```

Log lines are sent directly via SSE without escaping. If a log line contains `\n`, it breaks the SSE protocol and could inject fake events. An attacker who can generate specific log messages (e.g., through crafted HTTP requests that get logged) could inject arbitrary SSE events.

**Impact**: SSE event injection could confuse monitoring dashboards or admin UIs consuming the log stream.

**Recommendation**: Escape newlines in log lines before sending via SSE, or split multi-line entries into multiple `data:` lines per the SSE specification.

---

## LOW

### L1 — No Rate Limiting on Reverse Proxy (CWE-770)

**File**: `internal/app/run.go:1027-1034`

The proxy middleware chain includes `PanicRecovery`, `RequestLogger`, `SecurityHeaders`, and `CORS` but no rate limiting. While the registry and admin API have rate limiting, the reverse proxy itself is unprotected.

**Impact**: An attacker can flood the proxy with requests, potentially overwhelming backend containers.

**Recommendation**: Add configurable rate limiting to the proxy middleware chain, at least per-IP.

---

### L2 — Admin API Accepts Token Without Bearer Prefix (CWE-287)

**File**: `internal/adapters/in/http/admin/middleware.go:73-77`

```go
token := auth
if strings.HasPrefix(auth, "Bearer ") {
    token = strings.TrimPrefix(auth, "Bearer ")
}
```

The admin middleware accepts any value in the `Authorization` header as a token, not just properly formatted `Bearer <token>`. This is non-standard and could lead to accidental token exposure if clients send tokens in unexpected formats.

**Recommendation**: Strictly require the `Bearer` prefix for the admin API to follow RFC 6750.

---

### L3 — Proxy Creates New ReverseProxy Per Request (CWE-400)

**File**: `internal/usecase/proxy/service.go:306-335`

A new `httputil.ReverseProxy` instance is created for every incoming request in `proxyToTarget()` and `proxyToRegistry()`. While the underlying transport is shared, this creates garbage collection pressure under high load.

**Recommendation**: Cache reverse proxy instances per target (in the target cache alongside the ProxyTarget struct).

---

## INFO

### I1 — Security Headers Override Backend Policies

**File**: `internal/adapters/in/http/middleware/security.go`

Gordon sets security headers (`X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, etc.) on all proxied responses. These are set before `next.ServeHTTP()` is called, and the proxy's `ModifyResponse` also adds headers. If a backend application needs different policies (e.g., embedding in an iframe for a legitimate use case), Gordon's headers will interfere.

**Recommendation**: Consider making security headers configurable per route, or set them only if the backend doesn't already set them.

---

### I2 — Deprecated Plain Password Support

**File**: `internal/app/run.go` (auth config resolution)

The configuration still supports a plain `password` field alongside the secure `password_hash` field. While warnings are logged, the fallback allows insecure configurations.

**Recommendation**: Plan a deprecation timeline and remove plain password support in a future major version.

---

## Positive Security Observations

The following security practices are well-implemented:

1. **SSRF Protection** (`ssrf.go`): Comprehensive blocked CIDR list covering IPv4/IPv6 private networks, loopback, link-local, and cloud metadata endpoints. DNS resolution failures are blocked by default (safe against DNS rebinding).

2. **Constant-Time Comparisons**: `subtle.ConstantTimeCompare` used for username and password checks throughout. Bcrypt for password hashing with cost 12.

3. **Path Traversal Prevention**: Multi-layer validation with regex + `..` checks + absolute path rejection + `filepath.Clean` + containment validation in domain secrets store.

4. **Command Injection Prevention**: The pass and sops providers validate paths with strict regexes before shell execution, and use `exec.CommandContext` with timeouts.

5. **File Permissions**: Consistent use of `0700` for directories and `0600` for sensitive files. Atomic writes with fsync for secrets.

6. **JWT Security**: Issuer validation, signing method verification (HMAC only), not-before claims, revocation checking, and fail-closed on store errors.

7. **Hop-by-hop Header Protection**: Using `Rewrite` instead of `Director` in the reverse proxy prevents the `Connection: Authorization` header stripping attack.

8. **Scoped Authorization**: Fine-grained scope system for both registry (per-repo pull/push) and admin API (per-resource read/write) with wildcard support.

9. **Trusted Proxy Support**: `clientip.go` properly validates proxy headers against configured trusted networks before trusting X-Forwarded-For.

10. **Internal Registry Credentials**: Regenerated on each start, stored with 0600 permissions in secure runtime directories, cleaned up on shutdown.

---

## Recommendations Summary (Priority Order)

| # | Severity | Finding | Status |
|---|----------|---------|--------|
| C1 | CRITICAL | Remove credentials from debug logs | **FIXED** |
| C2 | CRITICAL | Bind container ports to 127.0.0.1 | **FIXED** |
| H1 | HIGH | Validate X-Forwarded-Host for realm URL | **FIXED** |
| H2 | HIGH | Trust X-Forwarded-Proto only from trusted proxies | **FIXED** |
| H3 | HIGH | Remove wildcard CORS from proxy | **FIXED** |
| H4 | HIGH | Use trusted-proxy-aware IP extraction in logging | **FIXED** |
| H5 | HIGH | Document auth-disabled admin API (intentional for local use) | **FIXED** (documented) |
| M1 | MEDIUM | Validate container ports belong to Gordon | TODO |
| M2 | MEDIUM | Add body size limit for proxied requests | **FIXED** |
| M3 | MEDIUM | Reject absolute paths in SOPS provider | **FIXED** |
| M4 | MEDIUM | Use singleflight for target resolution | TODO |
| M5 | MEDIUM | Remove unreliable hostname-length container detection | **FIXED** |
| M6 | MEDIUM | Escape SSE log lines | **FIXED** |
| L1 | LOW | Add rate limiting to proxy | TODO |
| L2 | LOW | Require Bearer prefix for admin API | **FIXED** |
| L3 | LOW | Cache reverse proxy instances | TODO |
| I1 | INFO | Security headers override backend policies | TODO |
| I2 | INFO | Deprecated plain password support | TODO |

---

## Remaining Work (TODO)

The following findings were not fixed because they require architectural changes,
new dependencies, or new configuration options that need design decisions.

### M1 — Validate Container Ports Belong to Gordon

**Why not fixed**: Requires maintaining a whitelist of Gordon-managed container ports
and cross-referencing during proxy target resolution. This is a non-trivial change
to the container lifecycle management that needs careful design to avoid race conditions
during zero-downtime deployments.

**Suggested approach**: Add a `ManagedPorts() map[int]string` method to `ContainerService`
that returns a map of host ports to container IDs. Check this map in `GetTarget()` when
resolving host-mode targets.

---

### M4 — Use Singleflight for Target Resolution

**Why not fixed**: Requires adding `golang.org/x/sync` as a new dependency.
The current TOCTOU race in the target cache is low-risk (duplicate resolution work,
not a security issue) but could cause brief inconsistencies during deployments.

**Suggested approach**:
```bash
go get golang.org/x/sync
```
Then wrap the target resolution in `GetTarget()` with `singleflight.Group.Do(domainName, ...)`.

---

### L1 — Add Rate Limiting to Reverse Proxy

**Why not fixed**: Requires a new configuration section (e.g., `[proxy.rate_limit]`)
and design decisions about defaults, per-route limits, and whether to share the
existing `ratelimit.MemoryStore` or create separate instances.

**Suggested approach**: Add `proxy.rate_limit.per_ip_rps` and `proxy.rate_limit.burst`
to the config, then add the rate limit middleware to the proxy chain in `createHTTPHandlers()`,
similar to how it's done for the admin API.

---

### L3 — Cache Reverse Proxy Instances

**Why not fixed**: Performance optimization, not a security fix. The current per-request
`httputil.ReverseProxy` allocation shares the transport so connection pooling still works.
Under extremely high load, the GC pressure from allocations could contribute to latency.

**Suggested approach**: Add a `proxy *httputil.ReverseProxy` field to `domain.ProxyTarget`
and create it once when the target is first resolved. Invalidate it when the target is
invalidated.

---

### I1 — Security Headers Override Backend Policies

**Why not fixed**: Design decision needed. The current behavior (always set security headers)
is the safer default. Making it configurable per route adds complexity to the config format.

**Suggested approach**: Either set headers in `ModifyResponse` only when the backend doesn't
already set them, or add a per-route `security_headers = false` option.

---

### I2 — Deprecated Plain Password Support

**Why not fixed**: This is a backwards-compatibility concern that should follow a deprecation
timeline. The warning is already logged when plain passwords are used.

**Suggested approach**: Add a deprecation notice in the next minor release notes, then
remove the plain password fallback in the next major version.
