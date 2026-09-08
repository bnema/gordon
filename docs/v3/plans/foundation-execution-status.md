# Foundation execution status

The accepted networking baseline is [ADR-006](../adr-006-pasta-pesto-publication.md): Podman 6 with required pasta/Pesto, no rootlessport fallback or host ingress. The [reference experiment](../../../dev/v3/proofs/podman6-pasta-pesto.md) demonstrates direct source preservation and dynamic forwarding without destination-container restart. It is not full Alpha 1 acceptance.

## Next work

1. Extend and review proposed [ADR-005](../adr-005-role-capabilities.md) for runtime-only Pesto access, directory recreation and engine-wide authority. F1 remains open.
2. Specify F2 single mapping ownership, bootstrap, current destination resolution, Podman/Pesto coexistence, withdrawal of established flows and crash/reboot reconciliation. Do not duplicate dynamic rules in static Quadlet publication.
3. Extend the existing proof harness for those contracts, container denial, UDP lifecycle and private pulls. Preserve the recorded AppArmor limitation until effective protection is demonstrated.
4. Define F3 installation/distribution contracts with the required helper stack and fail-closed prerequisite validation; implement only Alpha 1B slices whose contracts/proofs are accepted.

The former proposal for a host lifecycle executor is not selected. Pesto dynamic control removes the need to recreate edge merely to change forwarding rules. The absence of `podman update` port mutation is not a blocker to this mechanism.

Direct forwarding of dedicated workload TCP/UDP ports could reduce edge's transport responsibilities further. It is a separate topology/lifecycle refinement: current accepted edge UDP epoch and cleanup responsibilities cannot silently disappear. Keep it distinct from the accepted dependency and publication decision until its ownership contract is decided.
