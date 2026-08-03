# Kubernetes ingress

Gordon's supported production topology is a single rootless Podman host, either monolith or the orchestrated four-role split. Gordon does not currently provide a Kubernetes controller, Helm chart, or supported migration path into Kubernetes.

Do not deploy Gordon with a host Docker/Podman socket mounted into a generic pod. In split mode engine authority belongs only to runtime, and the implemented migration/lifecycle contracts depend on the rootless Podman component network, generated role manifests, and durable local checkpoint.

If Kubernetes or an ingress controller is your external TLS boundary, treat this page as architecture guidance only:

- terminate public TLS before the generated split edge, whose role config uses external TLS mode;
- expose only edge/public admin surfaces required by policy;
- keep component gRPC and Gordon runtime Unix RPC private;
- route registry requests through edge, which forwards to `gordon-registry` on the private component network, never loopback;
- do not translate Gordon labels into Kubernetes ownership claims.

Use [Split mode](/docs/operations/split-mode.md) and the [rootless migration runbook](/docs/operations/migration.md) for supported deployments.
