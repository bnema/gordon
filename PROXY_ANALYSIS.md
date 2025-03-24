# Proxy Server Analysis: `feat/templ-integration` vs `dev` Branch

## Key Issue
The proxy server in the `feat/templ-integration` branch is no longer receiving requests on both HTTP and HTTPS ports, while it worked properly in the `dev` branch.

## Root Causes

1. **Server Binding Method Changed**:
   - **dev branch (working)**: Uses standard approach with `httpServer.ListenAndServe()` and `httpsServer.ListenAndServeTLS("", "")`
   - **templ-integration branch (broken)**: Uses explicit IPv4 binding with `net.Listen("tcp4", "0.0.0.0:port")` first, then falls back if that fails

2. **Port Configuration Modifications**:
   - **templ-integration branch** added a new `configureProxyPorts()` function that automatically changes privileged ports to non-privileged ones for non-root users
   - Port 80 → 8080 and port 443 → 8443 when running outside containers as non-root

3. **Enhanced Admin Route Handling**:
   - More complex admin route handling with several verification steps and recovery mechanisms
   - Multiple connection tests and fallbacks can create race conditions

4. **Port Conflict Detection**:
   - More aggressive conflict detection which might be incorrectly disabling the proxy

## Specific Code Differences

### Server Binding
```go
// templ-integration (broken)
// First try explicit IPv4 binding
httpsAddr := fmt.Sprintf("0.0.0.0:%s", p.config.Port)
tcpListener, err := net.Listen("tcp4", httpsAddr)

if err != nil {
    logger.Warn("Failed to create HTTPS listener...")
    // Try simple binding as fallback
    if err := httpsServer.ListenAndServeTLS("", ""); err != nil {
        logger.Error("HTTPS server failed", "error", err)
    }
    return
}

// Create TLS listener
tlsListener := tls.NewListener(tcpListener, httpsServer.TLSConfig)
if err := httpsServer.Serve(tlsListener); err != nil {
    logger.Error("HTTPS server failed", "error", err)
}

// dev branch (working)
logger.Info("Starting HTTPS server", "address", httpsServer.Addr)
// Using empty strings for cert and key files since we're using GetCertificate
if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
    logger.Error("HTTPS server failed", "error", err)
}
```

### Testing and Verification
The `templ-integration` branch adds extensive connection testing code:
- Port binding verification
- Multiple hostname tests
- Complex fallback mechanisms
- Periodic admin route verification

## Recommended Fix

To fix the issue, we should:

1. Revert to the simple binding method from the `dev` branch in both `startHTTPSServer()` and `startHTTPServer()` functions
2. Simplify the admin connection testing logic
3. Keep the port conflict detection but ensure it doesn't incorrectly disable the proxy
4. Review the container environment detection to ensure it works properly

The simplest fix would be to modify the server binding methods to use the direct approach from the `dev` branch that was working correctly.