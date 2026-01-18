# Kubernetes Ingress Setup

This guide covers deploying Gordon behind a Kubernetes ingress controller for TLS termination.

## Overview

In Kubernetes, an **Ingress Controller** handles external traffic:
- Terminates TLS connections
- Routes requests to services based on hostname/path
- Sets `X-Forwarded-For` header with the client's real IP

Common ingress controllers: **nginx-ingress**, **Traefik**, **Contour**, **Kong**.

## Architecture

```
Client → Ingress Controller (HTTPS/443) → Gordon Service (HTTP) → Gordon Pod
                    ↓
              TLS terminated
              X-Forwarded-For set
```

## Understanding Pod Network CIDR

Kubernetes assigns each pod an IP from the **pod network CIDR**. When the ingress controller forwards requests to Gordon, it comes from the ingress pod's IP.

**To find your pod network CIDR:**

```bash
# Method 1: Check cluster-info
kubectl cluster-info dump | grep -m 1 cluster-cidr

# Method 2: Check CNI config (varies by CNI plugin)
kubectl get cm -n kube-system kubeadm-config -o yaml | grep podSubnet

# Method 3: Check a pod's IP to infer the range
kubectl get pods -o wide
```

**Common defaults by CNI:**

| CNI Plugin | Default Pod CIDR |
|------------|------------------|
| Flannel | `10.244.0.0/16` |
| Calico | `192.168.0.0/16` |
| Weave | `10.32.0.0/12` |
| Cilium | `10.0.0.0/8` |
| GKE | `10.0.0.0/14` or `/9` |
| EKS (VPC CNI) | VPC CIDR |

## Gordon Configuration

```toml
[api.rate_limit]
enabled = true
# Trust pod network so Gordon reads X-Forwarded-For from ingress
trusted_proxies = ["10.244.0.0/16"]  # Replace with your pod CIDR
```

## Ingress Controller Configuration

The ingress controller must be configured to forward the real client IP.

### nginx-ingress

Create or update the ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ingress-nginx-controller
  namespace: ingress-nginx
data:
  # Enable X-Forwarded-For processing
  use-forwarded-headers: "true"
  # Trust these CIDRs to set X-Forwarded-For
  # Include your cloud provider's load balancer IPs
  proxy-real-ip-cidr: "0.0.0.0/0"  # Or specific LB CIDR
```

If you have a cloud load balancer in front of nginx-ingress (common in EKS, GKE):

```yaml
data:
  use-forwarded-headers: "true"
  use-proxy-protocol: "false"  # Set to "true" if using NLB with proxy protocol
  proxy-real-ip-cidr: "10.0.0.0/8,192.168.0.0/16"
```

### Traefik

In your Traefik configuration:

```yaml
# traefik.yaml or Helm values
entryPoints:
  web:
    address: ":80"
    forwardedHeaders:
      trustedIPs:
        - "10.0.0.0/8"
        - "192.168.0.0/16"
  websecure:
    address: ":443"
    forwardedHeaders:
      trustedIPs:
        - "10.0.0.0/8"
        - "192.168.0.0/16"
```

## Complete Example

### Gordon Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gordon
spec:
  replicas: 1
  selector:
    matchLabels:
      app: gordon
  template:
    metadata:
      labels:
        app: gordon
    spec:
      containers:
      - name: gordon
        image: your-registry/gordon:latest
        ports:
        - containerPort: 80
        - containerPort: 5000
        volumeMounts:
        - name: config
          mountPath: /etc/gordon
        - name: docker-sock
          mountPath: /var/run/docker.sock
      volumes:
      - name: config
        configMap:
          name: gordon-config
      - name: docker-sock
        hostPath:
          path: /var/run/docker.sock
---
apiVersion: v1
kind: Service
metadata:
  name: gordon
spec:
  selector:
    app: gordon
  ports:
  - name: http
    port: 80
    targetPort: 80
  - name: registry
    port: 5000
    targetPort: 5000
```

### Gordon ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: gordon-config
data:
  gordon.toml: |
    [server]
    port = 80
    registry_port = 5000
    gordon_domain = "gordon.example.com"

    [api.rate_limit]
    enabled = true
    trusted_proxies = ["10.244.0.0/16"]

    [auth]
    enabled = false  # Configure auth as needed
```

### Ingress Resource

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: gordon
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "0"  # Unlimited for image uploads
spec:
  ingressClassName: nginx
  tls:
  - hosts:
    - gordon.example.com
    secretName: gordon-tls
  rules:
  - host: gordon.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: gordon
            port:
              number: 80
```

## Verifying the Setup

1. Deploy Gordon and the ingress
2. Make a request through the ingress
3. Check Gordon logs:

```bash
kubectl logs -l app=gordon | grep "client_ip"
```

If you see the ingress pod's IP instead of the client's IP:
- Verify `trusted_proxies` includes your pod CIDR
- Verify ingress controller has `use-forwarded-headers: "true"`

## Cloud-Specific Notes

### GKE

GKE uses a VPC-native pod network. Find the pod CIDR in:
- Console → Kubernetes Engine → Cluster → Networking → Pod address range

```toml
trusted_proxies = ["10.0.0.0/14"]  # GKE default
```

### EKS

EKS with VPC CNI assigns pod IPs from your VPC subnets:

```toml
trusted_proxies = ["10.0.0.0/16"]  # Your VPC CIDR
```

### AKS

AKS default pod CIDR:

```toml
trusted_proxies = ["10.244.0.0/16"]  # Azure CNI default
```

## Troubleshooting

### All requests show the same IP

**Symptom:** Rate limiting ineffective, logs show ingress pod IP.

**Cause:** `trusted_proxies` missing pod CIDR or ingress not forwarding headers.

**Fix:**
1. Add pod CIDR to `trusted_proxies`
2. Enable `use-forwarded-headers` in ingress controller

### X-Forwarded-For contains multiple IPs

**Symptom:** `X-Forwarded-For: 203.0.113.50, 10.0.1.5, 10.244.0.3`

This is normal when multiple proxies are involved (cloud LB → ingress → Gordon). Gordon takes the **first** IP (leftmost), which is the original client.

### Client IP shows cloud load balancer

**Symptom:** Client IP is AWS/GCP/Azure LB IP, not real client.

**Cause:** Cloud LB not configured to forward client IP.

**Fix:** Configure cloud LB to preserve or pass client IP (varies by provider).

## Related

- [Rate Limiting Configuration](../../docs/config/rate-limiting.md)
- [AWS ALB Guide](./aws-alb.md)
- [Running Gordon in a Container](./running-in-container.md)
