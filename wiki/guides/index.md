# Guides

In-depth guides for specific Gordon setups and integrations.

## Server Setup

- [Secure VPS Setup](./secure-vps-setup.md) - Tailscale SSH, firewalld, and CrowdSec
- [Running Gordon in a Container](./running-in-container.md) - Deploy Gordon itself in Docker or Podman
- [Podman Rootless Setup](./podman-rootless.md) - Enhanced security with rootless containers

## TLS & Load Balancing

- [AWS Application Load Balancer](./aws-alb.md) - Deploy behind AWS ALB
- [Kubernetes Ingress](./kubernetes-ingress.md) - Deploy behind Kubernetes ingress controller

## Remote Management

- [Remote CLI Management](./remote-cli.md) - Manage Gordon instances remotely

## Secrets Management

- [Using Pass for Secrets](./secrets-pass.md) - Unix password manager integration
- [Using SOPS for Secrets](./secrets-sops.md) - Encrypted secrets with SOPS

## Prerequisites

These guides assume you have:

1. Basic Linux administration experience
2. Gordon installed (see [Installation](/docs/installation.md))
3. Domain name available for configuration
