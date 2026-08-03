# Edge Role Configuration

`gordon serve --role edge --config edge.toml` accepts an **edge-only** TOML file. It rejects all unknown sections and keys, including the normal `[auth]`, `[runtime]`, and `[server]` sections, so an edge process cannot load control-plane secrets.

## TLS contract

TLS termination is explicit. There is no plaintext default. Split edges support only operator-provided certificate files or explicit external TLS termination. Gordon-managed ACME issuance and challenge handling remain monolith-only until a certificate/challenge delivery protocol exists; an edge never silently falls back from ACME to another mode.

- `mode = "files"` requires a certificate and key; Gordon serves HTTPS itself on streamed TLS-capable entrypoints. Gordon checks the certificate and key on each new TLS handshake and atomically adopts a valid matching replacement without restart. During a partial or invalid write it continues serving the last-known-good certificate, but `/healthz` returns `503` until a valid pair is present again. Existing TLS connections are not interrupted.
- `mode = "external"` permits the dedicated plaintext HTTP listener only from `trusted_proxy_cidrs`. Put the terminating load balancer or reverse-proxy CIDRs in that list. Direct HTTP connections are rejected, and forwarded client addresses are trusted only from those CIDRs. TLS-capable streamed entrypoints may only use TLS passthrough in this mode; HTTP TLS fallback fails startup rather than silently serving plaintext.

`/healthz` returns `204` only when both route and traffic streams are connected, the latest accepted traffic generation has applied successfully, the traffic manager reports its last reload healthy, and (for `files` mode) the certificate pair is valid. It returns `503` without internal error details otherwise.

The edge receives a separate authenticated, sanitized traffic graph after its route snapshot. It owns the graph's `tls_mux`, `smart_tcp`, TCP, and UDP listeners; duplicate or conflicting listener addresses fail startup. Backends must be explicit aliases or non-loopback reachable addresses. The edge never reads full control configuration, runtime state, or token stores beyond its configured control token.

The compatibility gate uses generated generic certificates only in test memory and verifies real HTTP, smart-TCP HTTPS fallback, `tls_mux` HTTPS termination, SNI TLS passthrough, raw TCP, and UDP sockets. It runs the listener matrix three times on Linux and uploads only protocol/status booleans—never listening ports, certificate material, or runtime IDs. This covers `mode = "files"` and `mode = "external"`; it does not imply split ACME support.

```toml
[control]
endpoint = "control.internal:9090"
token_env = "GORDON_EDGE_TOKEN"
# Explicit private-network development opt-in only:
insecure_tls = false

[edge]
listen_address = ":8443"
trusted_proxy_cidrs = ["10.0.0.0/8"]
max_concurrent_connections = 10000

[edge.tls]
mode = "external"

[logging.access_log]
enabled = true
format = "json"
output = "stdout"
```

For local TLS termination, replace the TLS section with:

```toml
[edge.tls]
mode = "files"
cert_file = "/run/gordon/tls/cert.pem"
key_file = "/run/gordon/tls/key.pem"
```

## Related

- [Logging](./logging.md)
- [Security Hardening](./security-hardening.md)
