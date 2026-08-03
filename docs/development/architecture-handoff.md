# Architecture handoff

Use this handoff for changes crossing control, runtime, edge, registry, or monolith boundaries.

Record: signed commit, affected role ownership, inbound/outbound ports, component-token scope, data/checkpoint migration, and the exact compatibility gate. State explicitly that only runtime has engine authority. Link the matching row in the [docs ownership matrix](../reference/all-container-refactor-docs-map.md).

Do not record socket endpoints, tokens, role environment values, private paths, or raw compatibility artifacts.
