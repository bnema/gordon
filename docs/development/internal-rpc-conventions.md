# Internal RPC conventions

Gordon's internal component RPCs use versioned protobuf contracts, gRPC, and scoped component bearer tokens.

## Protobuf layout and generation

- Sources are `api/proto/gordon/common/v1/common.proto` and `api/proto/gordon/runtime/v1/runtime.proto`.
- Generated Go bindings are committed under `api/gordon/common/v1/` and `api/gordon/runtime/v1/`.
- Proto packages are `gordon.common.v1` and `gordon.runtime.v1`.
- Their `go_package` values are `github.com/bnema/gordon/api/gordon/common/v1;commonv1` and `github.com/bnema/gordon/api/gordon/runtime/v1;runtimev1`; Go imports use the `commonv1` and `runtimev1` aliases.
- `buf.yaml` defines `api/proto` as the module. `buf.gen.yaml` runs the local `protoc-gen-go` and `protoc-gen-go-grpc` plugins with source-relative paths into `api/`.

Run `make proto` after changing a contract. Do not edit generated bindings by hand. Commit the source and regenerated bindings together. Run `make proto-check` to regenerate and fail if tracked or untracked files under `api/gordon` differ from the committed output.

## RPC tests

RPC adapter tests use `internal/testutils/grpctest`. Its `NewHarness` fixture uses `google.golang.org/grpc/test/bufconn`, registers services directly, owns client connections, and closes all resources through test cleanup. RPC tests do not open TCP ports or start application wiring when the behavior can be tested with this harness.

Use `AuthenticatedConn` for authenticated calls and `Conn` when testing missing credentials. `NewAuthFixture` supplies a deterministic local validator and identity; its token material is test-only.

## Component authentication

Every component gRPC server installs both `ComponentAuthUnaryInterceptor` and `ComponentAuthStreamInterceptor`. Every full RPC method name must have a non-empty entry in that service's method-to-scope map. Authentication fails closed when the validator is absent, the method is unmapped, the required scope is empty, bearer metadata is missing or malformed, validation fails, or validation returns no identity. Unknown methods are never implicitly authorized.

The runtime service currently maps methods as follows:

| RuntimeService method | Required scope |
| --- | --- |
| `ApplyCommand` | `runtime:deploy` |
| `WatchActualState` | `runtime:status` |
| `GetHealth` | `runtime:status` |
| `StreamLogs` | `runtime:logs` |
| `ListVolumes` | `runtime:status` |
| `RemoveVolume` | `runtime:deploy` |
| `ListImages` | `runtime:status` |
| `PruneImages` | `runtime:deploy` |
| `RuntimeSelfUpdate` | `runtime:selfupdate` |
| `ReportEdgeDrain` | `runtime:drain:ack` |

`ApplyCommand` additionally requires `runtime:reconcile` through `RequireScope` when its payload is a reconcile command.

After validation, the interceptors place `domain.ComponentIdentity` in the request or stream context. Handlers retrieve it with `ComponentIdentityFromContext` and enforce additional role or scope checks with `RequireComponentRole` and `RequireScope`. Handlers must not reconstruct identity from request fields.

Clients attach `authorization: Bearer ...` metadata with `NewBearerTokenCredentials` from `internal/adapters/out/grpc/auth`. These per-RPC credentials require transport security. `NewInsecureBearerTokenCredentials` exists only for the explicit private plaintext transport described below.

## Token lifecycle

Manage component tokens with these commands:

- `gordon auth component-token create`
- `gordon auth component-token list`
- `gordon auth component-token revoke <key-id>`

Create returns the plaintext token once. Gordon persists only its hash and safe metadata. List returns metadata only and never returns plaintext or the stored hash. Revoke marks the key ID as revoked; validation also rejects expired tokens and tokens missing the required scope. Plaintext tokens and token hashes must not appear in logs, list output, or documentation examples.

## Transport posture

TLS is the default for component clients. `runtime.endpoint` uses TLS and secure bearer credentials unless `runtime.insecure` is explicitly set to `true`.

Plaintext is permitted only as an explicit opt-in for scoped component traffic crossing an explicitly configured private rootless Podman network. Set `runtime.insecure: true` only for that deployment shape; it selects plaintext gRPC transport and the explicitly insecure bearer credentials. Do not use this exception on public, shared, or otherwise untrusted networks. `runtime.token` or `runtime.token_env` supplies the component token, and `runtime.listen_address` configures the runtime server listener.
