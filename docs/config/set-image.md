# Promotion Policies

Promotion runs one pushed image digest in one service of an existing app.
Push never deploys. Configure grants at the top level of `gordon.toml`:

```toml
[[promotion_policies]]
issuer = "https://issuer.example.com"
subjects = ["repo:acme/web:production"]
app = "web"
service = "web"
repository = "web"
```

| Key | Description |
|-----|-------------|
| `issuer` | Exact declared `trusted_issuers` issuer, or `gordon` for stored-token callers |
| `subjects` | Non-empty list of exact subject strings; any listed subject matches |
| `app` | One app name; validated as a name, not checked against existing apps |
| `service` | One service name in that app |
| `repository` | One registry repository path, without host, tag, or digest |

All keys are required. There are no wildcard or prefix matches. Whitespace
and Unicode in subjects are not normalized. App, service, and repository
names must be valid; unknown keys and duplicate `(issuer, subject, app,
service)` grants are rejected, even with different repositories.
Invalid configuration at startup fails; an invalid reload keeps the previous
snapshot. An empty policy set denies non-owner promotions.

The local owner (`local-owner`, via the owner-only admin socket) can promote
without a policy. Admin bearer tokens require `apps:write` permission **and**
a policy with `issuer = "gordon"` and their token subject; admin scope alone
is not a promotion grant. OIDC principals are recorded as
`oidc:<iss>#<sub>`; stored-token principals as `gordon:<sub>`.

The image must already exist in this installation's registry and use
`REPO@sha256:<64 lowercase hex digits>`. An optional host must be the main,
CI, or a legacy registry host; Gordon canonicalizes it to the main host.
Tags and external registries are refused. The app must not be stopped, and
services other than the target must match desired state. Promotion changes
only the target image, preserving other services' provenance. An already
active desired digest succeeds without changing state.

## Related

- [Authentication](./auth.md#oidc-trusted-issuers)
- [CI Deployment](../deployment/generic-ci.md)
- [Apps Commands](../cli/apps.md#gordon-apps-promote)
