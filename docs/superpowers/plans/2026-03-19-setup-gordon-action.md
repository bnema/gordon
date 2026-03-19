# setup-gordon Action Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a composite GitHub Action that installs the Gordon CLI binary into the runner's PATH.

**Architecture:** Single composite action with a shell script that detects OS/arch, downloads the correct release archive, extracts the binary, and verifies it works. Documentation updated to reference the action.

**Tech Stack:** GitHub Actions composite, bash, curl, tar

---

### Task 1: Create the setup-gordon composite action

**Files:**
- Create: `.github/actions/setup-gordon/action.yml`

**Context:**
- Official releases are `.tar.gz` archives: `gordon_{os}_{arch}.tar.gz` (e.g. `gordon_linux_amd64.tar.gz`)
- Archives contain a single `gordon` binary at root
- Release URL pattern: `https://github.com/bnema/gordon/releases/download/{tag}/gordon_{os}_{arch}.tar.gz`
- Latest release tag can be resolved via: `gh release view --json tagName -q .tagName` or curl redirect on `/releases/latest`
- Supported OS: linux, darwin. Supported arch: amd64, arm64.
- Runner arch mapping: `X64` → `amd64`, `ARM64` → `arm64`

- [ ] **Step 1: Create action.yml**

```yaml
name: 'Setup Gordon'
description: 'Install the Gordon CLI binary into the runner PATH'
author: 'Gordon Contributors'

branding:
  icon: 'download-cloud'
  color: 'blue'

inputs:
  version:
    description: 'Gordon version to install (e.g. "v2.0.0" or "latest")'
    required: false
    default: 'latest'

outputs:
  version:
    description: 'The version that was installed'
    value: ${{ steps.install.outputs.version }}
  path:
    description: 'Path to the installed binary'
    value: ${{ steps.install.outputs.path }}

runs:
  using: 'composite'
  steps:
    - name: Install Gordon CLI
      id: install
      shell: bash
      run: |
        set -euo pipefail

        # Determine OS
        case "$RUNNER_OS" in
          Linux)  OS="linux" ;;
          macOS)  OS="darwin" ;;
          *)      echo "::error::Unsupported OS: $RUNNER_OS"; exit 1 ;;
        esac

        # Determine architecture
        case "$RUNNER_ARCH" in
          X64)    ARCH="amd64" ;;
          ARM64)  ARCH="arm64" ;;
          *)      echo "::error::Unsupported architecture: $RUNNER_ARCH"; exit 1 ;;
        esac

        # Resolve version
        VERSION="${{ inputs.version }}"
        if [[ "$VERSION" == "latest" ]]; then
          VERSION=$(curl -fsSL -o /dev/null -w '%{redirect_url}' "https://github.com/bnema/gordon/releases/latest" | grep -oP '[^/]+$')
          if [[ -z "$VERSION" ]]; then
            echo "::error::Failed to resolve latest Gordon version"
            exit 1
          fi
        fi

        echo "Installing Gordon ${VERSION} (${OS}/${ARCH})..."

        # Download and extract
        ARCHIVE="gordon_${OS}_${ARCH}.tar.gz"
        URL="https://github.com/bnema/gordon/releases/download/${VERSION}/${ARCHIVE}"
        INSTALL_DIR="/usr/local/bin"

        curl -fsSL "$URL" -o "/tmp/${ARCHIVE}"
        tar -xzf "/tmp/${ARCHIVE}" -C "$INSTALL_DIR" gordon
        chmod +x "${INSTALL_DIR}/gordon"
        rm -f "/tmp/${ARCHIVE}"

        # Verify
        INSTALLED_VERSION=$("${INSTALL_DIR}/gordon" version 2>&1 || echo "unknown")
        echo "Gordon installed: ${INSTALLED_VERSION}"

        # Set outputs
        echo "version=${VERSION}" >> "$GITHUB_OUTPUT"
        echo "path=${INSTALL_DIR}/gordon" >> "$GITHUB_OUTPUT"
```

- [ ] **Step 2: Commit**

```bash
git add .github/actions/setup-gordon/action.yml
git commit -m "feat: add setup-gordon composite GitHub Action"
```

---

### Task 2: Update GitHub Actions deployment documentation

**Files:**
- Modify: `docs/deployment/github-actions.md`

**Context:**
- Replace all manual `curl + chmod` install blocks with `uses: bnema/gordon/.github/actions/setup-gordon@main`
- Keep the rest of each workflow identical (checkout, build-and-deploy steps, env vars)
- Add a dedicated section documenting the action inputs/outputs
- All examples must use `${{ secrets.GORDON_REMOTE }}` for the remote URL

- [ ] **Step 1: Replace "Install Gordon Binary" section (lines 37-46)**

Replace the manual curl instructions with:

```markdown
### 3. Setup Gordon CLI

Add this step to your workflow to install the Gordon binary:

\```yaml
- name: Setup Gordon
  uses: bnema/gordon/.github/actions/setup-gordon@main
  # with:
  #   version: latest  # optional, default: latest
\```
```

- [ ] **Step 2: Update "Deploy on Tag Push" example (lines 54-79)**

Replace the `Install Gordon` step with the setup-gordon action step.

- [ ] **Step 3: Update "Continuous Deploy on Main" example (lines 85-114)**

Replace the `Install Gordon` step with the setup-gordon action step.

- [ ] **Step 4: Update "Manual Dispatch" example (lines 120-152)**

Replace the `Install Gordon` step with the setup-gordon action step.

- [ ] **Step 5: Update "Monorepo" example (lines 158-195)**

Replace the `Install Gordon` step with the setup-gordon action step.

- [ ] **Step 6: Add setup-gordon Action Reference section**

After the "Recommended: gordon push" section and before "Alternative: Docker-based Workflow", add:

```markdown
### setup-gordon Action Reference

#### Inputs

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `version` | No | `latest` | Gordon version to install (`v2.0.0`, `latest`) |

#### Outputs

| Output | Description |
|--------|-------------|
| `version` | The version actually installed |
| `path` | Path to the installed binary |
```

- [ ] **Step 7: Commit**

```bash
git add docs/deployment/github-actions.md
git commit -m "docs: update GitHub Actions guide to use setup-gordon action"
```
