## Release and split-mode checklist

- [ ] I identified the affected row(s) in `docs/reference/all-container-refactor-docs-map.md` and updated their canonical documentation.
- [ ] I ran the exact owner gate(s) from that matrix and recorded results.
- [ ] Security changes include an executable blocking test; no pending scenario is claimed as coverage.
- [ ] Compatibility reports identify the immutable old-side commit and a distinct candidate commit.
- [ ] Workflow actions and container bases are immutable SHA/digest pins with version comments.
- [ ] Release changes pass `make release-check` and non-publishing `make release-smoke` before publishing is enabled.
- [ ] No tokens, role environment files, socket paths, or raw private artifacts are included in this PR.
- [ ] Tests were written/updated first, commits are signed, and `git status --short` is clean.
