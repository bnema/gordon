# Edge Role Configuration

`gordon serve --role edge --config edge.toml` accepts an **edge-only** TOML file. It rejects all unknown sections and keys, including the normal `[auth]`, `[runtime]`, and `[server]` sections, so an edge process cannot load control-plane secrets.

## TLS contract

TLS termination is explicit. There is no plaintext default.

- `mode = "files"` requires a certificate and key; Gordon serves HTTPS itself.
- `mode = "external"` permits HTTP only from `trusted_proxy_cidrs`. Put the terminating load balancer or reverse-proxy CIDRs in that list. Direct HTTP connections are rejected, and forwarded client addresses are trusted only from those CIDRs.

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
