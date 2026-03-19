# Design: `setup-gordon` GitHub Action

**Date:** 2026-03-19
**Status:** Approved

## Goal

Composite GitHub Action that installs the Gordon CLI binary into the runner's PATH. Setup only — no execution.

## Location

`.github/actions/setup-gordon/action.yml`

## Inputs

| Input     | Default  | Description                              |
|-----------|----------|------------------------------------------|
| `version` | `latest` | Version to install (`v2.0.0`, `latest`)  |

## Outputs

| Output    | Description                        |
|-----------|------------------------------------|
| `version` | The version actually installed     |
| `path`    | Path to the installed binary       |

## Logic

1. Determine runner OS and arch (`linux`/`darwin`, `amd64`/`arm64`)
2. If `version=latest`, resolve the tag via GitHub API redirect
3. Download `gordon_{os}_{arch}` from GitHub Releases
4. Place binary at `/usr/local/bin/gordon`, `chmod +x`
5. Verify with `gordon version`

## Usage

```yaml
steps:
  - uses: bnema/gordon/.github/actions/setup-gordon@main
    with:
      version: latest

  - run: gordon push --build --remote "$GORDON_REMOTE" --no-confirm
    env:
      GORDON_TOKEN: ${{ secrets.GORDON_TOKEN }}
      GORDON_REMOTE: ${{ secrets.GORDON_REMOTE }}
```

## Documentation updates

- Update `docs/deployment/github-actions.md` to document `setup-gordon` as the recommended approach

## Out of scope

- No Docker login/setup (Gordon handles its own auth)
- No command execution
- No Windows support (Gordon does not build for Windows)
- The existing `.github/actions/deploy/` action remains untouched
