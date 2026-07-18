# Split-mode agent handoff

Record only reproducible, non-sensitive facts:

- branch and signed commit IDs;
- exact commands run and their exit status;
- runtime type/rootless status, never socket credentials;
- migration phase and redacted checkpoint path;
- failing gate and focused rerun command;
- whether the tree is clean.

Do not paste role environment files, tokens, Authorization headers, private host paths, raw compatibility artifacts, or secret-provider output. Preserve migration checkpoints/outboxes for diagnosis. A handoff must explicitly state whether public listener authority is monolith, prepared, or switched and whether runtime authority transferred.
