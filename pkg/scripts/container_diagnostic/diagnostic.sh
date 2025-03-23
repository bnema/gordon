#!/bin/bash

# Gordon Container Diagnostic Script
# Usage: podman exec -it gordon /bin/sh /diagnostic.sh

echo "----- Gordon System Information -----"
echo "Date: $(date)"
echo "Hostname: $(hostname)"
echo "Container ID: $(hostname)"

echo "----- Network Interfaces -----"
ip addr show

echo "----- Network Routes -----"
ip route show

echo "----- Listening Ports -----"
netstat -tuln

echo "----- Process Listening On Ports -----"
netstat -tulnp

echo "----- HTTP Server Test -----"
curl -v http://localhost:8080/admin/ping 2>&1 | grep -v '^{.*}'

echo "----- HTTPS/HTTP Binding Test -----"
# Try to create listeners on ports 80, 443, and 8080 to test if they're available
# More reliable port check using timeout
check_port() {
    local port=$1
    local service=$2
    # Try to bind to the port briefly
    timeout 1 bash -c "nc -l -p $port >/dev/null 2>&1" >/dev/null 2>&1
    if [ $? -eq 124 ] || [ $? -eq 1 ]; then
        # Timeout or error means port is likely in use
        echo "✅ Port $port is in use - $service server is likely running"
        echo "Process using port $port:"
        # Multiple ways to check what's using the port
        lsof -i :$port 2>/dev/null || \
        ss -tulpn | grep ":$port" 2>/dev/null || \
        netstat -tulpn | grep ":$port" 2>/dev/null || \
        ls -l /proc/*/fd/* 2>/dev/null | grep -i "0.0.0.0:$port" | head -5
    else
        echo "❌ WARNING: Port $port is NOT in use - $service server is not running!"
        # Get the Gordon PID
        GORDON_PID=$(pgrep -f "/gordon serve" || pgrep -f "gordon$" || echo "")
        if [ -n "$GORDON_PID" ]; then
            echo "Gordon process found (PID: $GORDON_PID)"
            
            # Check if anything is preventing binding to the port
            echo "Checking process capabilities:"
            grep Cap /proc/$GORDON_PID/status || echo "Could not check process capabilities"
            
            # Check if the process tried to bind but failed (search logs)
            echo "Checking for binding errors in Gordon logs for port $port:"
            grep -i "error\|fail\|warn" /proc/1/fd/1 | grep -i "server\|listen\|bind\|port $port" | tail -10 || echo "No specific binding errors found in logs"
            
            # Try to get socket information
            echo "Socket status for port $port:"
            ss -tulpen | grep ":$port" || echo "No socket listening on port $port"
            
            # Check port availability externally
            echo "Testing if port can be bound explicitly:"
            timeout 2 nc -l -p $port -v 2>&1 || echo "Could not bind to port $port"
        else
            echo "Gordon process not found"
        fi
    fi
    echo ""
}

# Check main ports
check_port 80 "HTTP"
check_port 443 "HTTPS"
check_port 8080 "Main HTTP"

# Additional detailed diagnostics for HTTPS/TLS
echo "----- Detailed TLS Server Diagnostics -----"
# Check for TLS configuration issues
echo "TLS configuration in logs:"
grep -i "TLS\|cert\|getCertificate\|fallback" /proc/1/fd/1 | tail -15 || echo "No TLS configuration logs found"

# Check for listener creation logs
echo "Listener creation logs:"
grep -i "listener\|starting.*server\|bound to" /proc/1/fd/1 | tail -15 || echo "No listener creation logs found"

# Check for server startup sequence issues
echo "Server startup sequence:"
grep -i "starting\|init\|ready\|signal" /proc/1/fd/1 | grep -v "DEBUG" | tail -20 || echo "No server startup sequence logs found"

# Check port binding status in container
echo "Port mapping configuration:"
cat /proc/1/environ | tr '\0' '\n' | grep -i "PORT\|GORDON_PROXY" || echo "No port mapping environment variables found"

echo "----- Privileges and Capabilities Check -----"
# Check process privileges
echo "Process privileges:"
id || echo "Could not get process ID"

# Check capabilities
echo "Process capabilities:"
if command -v capsh >/dev/null 2>&1; then
    capsh --print || echo "Could not run capsh"
else
    grep Cap /proc/self/status || echo "Could not check capabilities via proc"
fi

# Check container security context
echo "Container security context:"
if [ -f "/.dockerenv" ] || [ -f "/run/.containerenv" ]; then
    echo "Running in a container"
    ls -la /proc/1/ns/ || echo "Could not check namespaces"
else
    echo "Not running in a container"
fi

echo "----- Running Gordon Processes -----"
ps aux | grep -i gordon | grep -v grep

echo "----- DNS Resolution Test -----"
# Try to resolve our own domain
DOMAIN=$(grep GORDON_HTTP_DOMAIN /proc/1/environ | cut -d= -f2 | tr -d '\0')
SUBDOMAIN=$(grep GORDON_HTTP_SUBDOMAIN /proc/1/environ | cut -d= -f2 | tr -d '\0')
if [ -n "$DOMAIN" ] && [ -n "$SUBDOMAIN" ]; then
    FQDN="${SUBDOMAIN}.${DOMAIN}"
    echo "Testing DNS resolution for $FQDN"
    dig $FQDN +short
    host $FQDN
else
    echo "Domain information not found in environment"
fi

echo "----- TLS Certificate Status -----"
CERT_DIR=/certs
if [ -d "$CERT_DIR" ]; then
    echo "Certificate directory contents:"
    ls -la $CERT_DIR
    
    # Check for certificates for our domain
    if [ -n "$FQDN" ]; then
        echo "Looking for certificates for $FQDN:"
        find $CERT_DIR -name "*$FQDN*" -o -name "cert-$FQDN"
    fi
else
    echo "Certificate directory $CERT_DIR not found"
fi

echo "----- Gordon HTTPS Server Logs -----"
echo "Last 20 log entries related to HTTPS server initialization:"
grep -i "server\|tls\|https\|Certificate\|net\." /proc/1/fd/1 | tail -20

echo "----- Goroutines Status -----"
# Check if GOMAXPROCS is set too high
echo "GOMAXPROCS setting:"
grep -i "GOMAXPROCS" /proc/1/environ | tr '\0' '\n' || echo "GOMAXPROCS not explicitly set"

# Check the number of threads used by gordon
echo "Number of threads used by Gordon process:"
pid=$(pgrep -f "^/gordon")
if [ -n "$pid" ]; then
    ls -l /proc/$pid/task | wc -l
else
    echo "Gordon process not found"
fi

echo "----- TCP Connection Status -----"
# Check existing TCP connections and states
echo "TCP connection states:" 
netstat -nat | awk '{print $6}' | sort | uniq -c

echo "----- End of Diagnostic -----" 