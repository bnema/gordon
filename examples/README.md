# Examples

Config is auto-generated on first `gordon start`. Use these examples as reference or replace the generated config:

```bash
cp examples/production.toml ~/.config/gordon/gordon.toml
```

## Configurations

| File | Use case |
|------|----------|
| [`minimal.toml`](minimal.toml) | Single app, no auth. Start here. |
| [`development.toml`](development.toml) | Local .local domains, unsafe secrets |
| [`staging.toml`](staging.toml) | Branch previews, token auth |
| [`production.toml`](production.toml) | Pinned versions, full logging |
| [`saas-multi-tenant.toml`](saas-multi-tenant.toml) | Customer subdomains, shared services |
| [`logging.toml`](logging.toml) | All logging options explained |

## Workflows

| File | Purpose |
|------|---------|
| [`github-workflow.yml`](github-workflow.yml) | Deploy on git tag push |
| [`rollback-workflow-example.md`](rollback-workflow-example.md) | Version management with manifests |

## Config Reference

See comments in each `.toml` file. Full docs: [README](../README.md#configuration)
