# Design documents

These are the specifications, plans, research notes, contracts, and task lists each
qurator feature was built from, kept as a record of why the code looks the way it does.
Every feature here is implemented and merged; the status line at the top of each `spec.md`
records when. They are not a roadmap, and the code and README are authoritative where
the two disagree.

| Directory | Feature |
|---|---|
| `001-qr-service-baseline/` | The v1 service: ephemeral and dynamic codes, auth, analytics, export/import, console. Includes the OpenAPI contract, error catalogue, data model, and stack research. |
| `002-direct-codes/` | Direct codes: persisted QR images whose content is the destination itself, bypassing redirection. |
| `003-storage-urls-batch/` | Direct storage URLs for code images and batch creation. |
| `004-first-code-reliability/` | Console sign-in throttling, visible validation errors, clipboard behaviour, and valid scan URLs. |
| `005-docker-packaging/` | The container image, compose file, and release workflow. |

The standing engineering rules distilled from these documents live in the repository
root [`AGENTS.md`](../../AGENTS.md).
