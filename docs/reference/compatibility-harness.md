# Compatibility harness

The compatibility harness compares Gordon behavior between an old baseline and the current checkout. It is for split-readiness work: each fixture declares the surfaces it exercises so reviewers can see which user-visible contracts are protected.

## Old and new

- **Old** is the baseline git ref checked out for comparison.
- **New** is the current working tree.
- The old baseline defaults to `origin/main`.
- Set `GORDON_COMPAT_BASELINE_REF=<ref>` to compare against a different branch, tag, or commit.

## Fixture metadata

Fixtures live under `internal/testutils/compatoldnew/fixtures` and are described by `compatoldnew.Fixture`:

- `Name`: stable scenario name.
- `ConfigPath`: TOML config fixture path.
- `EnvFiles`: optional env files loaded for the scenario.
- `ExpectedSurfaces`: touched compatibility surfaces.
- `PodmanRequired`: whether the scenario requires a real Podman runtime.

Available surface tags are:

- `config`
- `cli`
- `api`
- `registry`
- `proxy`
- `runtime`
- `migration`
- `security`

Every fixture must declare the surfaces it intends to touch. If a change affects a new surface, update the fixture declaration in the same change so reviewers can see the expanded compatibility contract.

## Podman scenarios

Podman-backed scenarios are opt-in because many development and CI environments do not provide a Podman binary or service.

- By default, fixtures with `PodmanRequired: true` skip with a clear reason.
- Set `GORDON_COMPAT_PODMAN=1` to run Podman-required fixtures.
- Non-Podman fixtures must not depend on a runtime socket.

## Golden updates

Golden compatibility outputs should be updated only when the new behavior is intentionally different and the difference is compatible with the split plan. When updating goldens:

1. Run the old/new comparison against the default `origin/main` baseline unless the review calls for another ref.
2. Use `GORDON_COMPAT_BASELINE_REF=<ref>` only to compare against a named release, branch, or specific commit.
3. Review the touched-surface declarations before accepting new goldens.
4. Keep fixture data generic: no private paths, real domains, or private credentials.

## Initial fixtures

The Phase 1 fixture set contains generic config/env inputs only:

- `minimal.toml`: smallest valid app route config.
- `realistic.toml`: config covering routes, preview settings, network isolation, volumes, auth placeholders, registry, proxy, runtime, and security surfaces.
- `legacy.toml`: deprecated config keys for migration compatibility.
- `invalid.toml`: malformed TOML for negative config/migration behavior.
- `basic.env`: generic environment values for harness setup.
