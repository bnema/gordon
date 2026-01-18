# AWS Application Load Balancer (ALB) Setup

This guide covers deploying Gordon behind AWS Application Load Balancer for TLS termination.

## Overview

AWS ALB is a Layer 7 load balancer that:
- Terminates TLS connections
- Forwards requests to your EC2 instances or ECS containers
- Automatically scales with traffic
- Sets `X-Forwarded-For` header with the client's real IP

## Architecture

```
Client → ALB (HTTPS/443) → Gordon (HTTP/80) → Containers
              ↓
         TLS terminated
         X-Forwarded-For set
```

## Understanding VPC CIDR

A **VPC** (Virtual Private Cloud) is your isolated network in AWS. Every VPC has a **CIDR** (Classless Inter-Domain Routing) block—the private IP range used by resources in that VPC.

ALB connects to Gordon from within your VPC, so Gordon sees the ALB's private IP, not the client's public IP. The client IP is passed via `X-Forwarded-For`.

**To find your VPC CIDR:**

1. Go to AWS Console → VPC → Your VPCs
2. Find the **IPv4 CIDR** column
3. Common values: `10.0.0.0/16`, `172.31.0.0/16`

## Gordon Configuration

```toml
[api.rate_limit]
enabled = true
# Trust your VPC CIDR so Gordon reads X-Forwarded-For from ALB
trusted_proxies = ["10.0.0.0/16"]  # Replace with your VPC CIDR
```

### Multiple VPCs or Subnets

If Gordon receives traffic from multiple VPCs (e.g., via VPC peering):

```toml
[api.rate_limit]
trusted_proxies = [
    "10.0.0.0/16",   # Production VPC
    "10.1.0.0/16",   # Staging VPC
]
```

## ALB Configuration

### Target Group Settings

When creating your ALB target group:

| Setting | Value |
|---------|-------|
| Target type | `instance` or `ip` |
| Protocol | HTTP |
| Port | 80 (or your Gordon port) |
| Health check path | `/health` or `/v2/` |

### Client IP Preservation

ALB automatically sets `X-Forwarded-For`. No special configuration needed for HTTP targets.

For **NLB** (Network Load Balancer) with TCP, you may need to enable Proxy Protocol v2:

```yaml
# Kubernetes annotation for AWS Load Balancer Controller
service.beta.kubernetes.io/aws-load-balancer-proxy-protocol: "*"
```

## Verifying the Setup

1. Make a request through the ALB
2. Check Gordon logs for the client IP:

```bash
# Should show real client IP, not ALB IP
journalctl -u gordon | grep "client_ip"
```

If you see the ALB's private IP (e.g., `10.0.x.x`) instead of the client's public IP, check that:
- `trusted_proxies` includes your VPC CIDR
- ALB is forwarding `X-Forwarded-For` (default behavior)

## Security Groups

Ensure your security groups allow:

| Source | Destination | Port | Purpose |
|--------|-------------|------|---------|
| `0.0.0.0/0` | ALB | 443 | HTTPS from internet |
| ALB SG | Gordon SG | 80 | ALB to Gordon |

## Terraform Example

```hcl
resource "aws_lb" "gordon" {
  name               = "gordon-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.public_subnets
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.gordon.arn
  port              = "443"
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.gordon.arn
  }
}

resource "aws_lb_target_group" "gordon" {
  name     = "gordon-tg"
  port     = 80
  protocol = "HTTP"
  vpc_id   = var.vpc_id

  health_check {
    path                = "/v2/"
    healthy_threshold   = 2
    unhealthy_threshold = 10
  }
}
```

## Troubleshooting

### All requests show the same IP

**Symptom:** Rate limiting doesn't work, logs show ALB IP for all requests.

**Cause:** `trusted_proxies` doesn't include ALB's IP range.

**Fix:** Add your VPC CIDR to `trusted_proxies`.

### 502 Bad Gateway

**Symptom:** ALB returns 502 errors.

**Cause:** Gordon health check failing or not listening.

**Fix:** Verify Gordon is running and health check path is correct.

## Related

- [Rate Limiting Configuration](../../docs/config/rate-limiting.md)
- [Running Gordon in a Container](./running-in-container.md)
