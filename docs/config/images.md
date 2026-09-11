# Images Configuration

Configure automatic image cleanup for Docker runtime images and local registry storage.

## Overview

When enabled, Gordon runs a scheduled image prune job that:

- Prunes dangling runtime images that carry positive Gordon provenance.
- Applies tag retention to registry repositories.
- Preserves the `latest` tag.
- Removes unreferenced blobs after tag cleanup.
- Reports every protected and unknown candidate with its reason.

The CLI `gordon images prune` runs the same use case and planner as the scheduled job, with the same defaults (keep `latest` + 3 previous tags, both scopes enabled: dangling runtime images and registry tag retention). App state existing on the server is normal and never disables pruning; the plan decides per candidate.

## Configuration

```toml
[images]
# Docker Hub, ghcr.io, quay.io, and Gordon's registry are already allowed.
allowed_registries = ["registry.internal:5000"]
require_digest = false

[images.prune]
enabled = false
schedule = "daily"
keep_last = 3
```

## Settings

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `images.allowed_registries` | array | `[]` | Additional registry hostname+port entries. Docker Hub (`docker.io`, canonical pull host `registry-1.docker.io`), `ghcr.io`, `quay.io`, and Gordon's configured registry are always allowed. Include non-default ports, e.g. `"registry.example.com:5000"`. Hostnames are case-insensitive; one trailing dot and port `443` are canonicalized. This setting does not configure registry credentials. |
| `images.require_digest` | bool | `false` | Require every image reference, including Gordon registry images, to use a valid `@sha256:<64 hex chars>` digest. |
| `images.prune.enabled` | bool | `false` | Enables scheduled image cleanup |
| `images.prune.schedule` | string | `"daily"` | Schedule preset: `hourly`, `daily`, `weekly`, `monthly` |
| `images.prune.keep_last` | int | `3` | Number of newest non-`latest` tags kept per repository during registry cleanup (`latest` is always kept when present) |

The policy validates registry names and strict SHA-256 digest syntax at manifest apply, digest resolution, deployment preflight, and immediately before each pull. It rejects malformed, userinfo-bearing, and ambiguous authorities. External app images currently require digest-pinned references, independently of `require_digest`; external tag resolution is not available. External pulls are anonymous, so adding a host to `allowed_registries` does not enable authenticated private-registry access. This hostname allowlist does **not** prove that DNS resolves to a public address and does not constrain runtime egress; enforce destination-level restrictions in the host firewall or runtime network policy.

## Retention Behavior

- `latest` is always preserved.
- `keep_last` applies per repository and counts non-`latest` tags.
- `keep_last` sets a minimum retained set, not a maximum: a tag named by durable app state, or referenced by the OCI closure of a protected manifest, survives beyond the window.
- `keep_last = 0` skips registry tag/blob cleanup entirely (runtime dangling prune still runs).
- Negative `keep_last` values are invalid.

## Prune Safety

A prune deletes only resources it can positively prove safe. Each candidate gets one verdict: `eligible` (deleted), `protected` (a durable fact claims it), or `unknown` (a required fact could not be read). Protected and unknown candidates are reported and left in place; an operation that deletes nothing is a success.

Durable protection covers the DESIRED and ACTIVE state of every app, including stopped and partially converged services; recovery inhibitions; staged and committed apply intents; unfinished operation journal entries; runtime container use for running and stopped containers; recent uploads; and the transitive OCI closure of every retained manifest. Durable digest roots are repository-qualified, so moving a tag away from a deployed image keeps that manifest's config, layers, child manifests, and subject content protected. An unreadable, unsupported, or unresolvable manifest makes the blobs whose safety depended on it `unknown` instead of eligible.

Registry content is repository-scoped: a blob is served only to a repository that completed an upload of that digest, and a manifest may only reference config, layers, and child manifests that repository owns. Image labels never authorize deletion of a runtime image; eligibility requires a matching released ownership claim from the app that pinned it, no container use, and a complete inventory.

An exclusive GC lease serializes prune against app apply, deploy, start, restart, remove, recovery, and restore. Those paths hold a shared lease from validation/resource acquisition through the durable publication of that resource's protection, and an apply verifies the expected desired revision so a stale apply cannot overwrite a newer one.

## Related

- [CLI Images Command](../cli/images.md)
- [Configuration Reference](./reference.md)
