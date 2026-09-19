# Tasks: First-code reliability

## Setup
- [x] T001 Write spec, clarification decisions, design and Constitution Check in `specs/004-first-code-reliability/`.
- [x] T002 Add isolated browser tooling and fixtures in `tests/browser/`.

## US1 — Printed-code correctness
- [x] T003 [US1] Add and run failing origin config/creation regressions in `internal/config/validate_test.go` and `tests/contract/scan_origin_test.go`.
- [x] T004 [US1] Implement shared origin validation, service guard and API/console error mapping in `internal/domain/scan_origin.go`, `internal/config/validate.go`, `internal/codes/service.go`, `internal/httpapi/`, `cmd/qurator/console_adapters.go`.

## US2 — Consistent sign-in protection
- [x] T005 [US2] Add and run failing shared-route throttle regression in `internal/httpapi/router_test.go`.
- [x] T006 [US2] Apply shared throttle to console sign-in in `internal/httpapi/router.go` and render console rejection in `internal/httpapi/middleware/ratelimit.go`.

## US3 — Recoverable browser lifecycle
- [x] T007 [US3] Write and run failing browser regression scenarios in `tests/browser/reliability.spec.js`.
- [x] T008 [US3] Implement visible expected errors, generic failures, version updates and stable controls in `internal/console/handler.go`, templates, and `internal/console/assets/app.js`.
- [x] T009 [US3] Fix clipboard outcome handling and form confirmation in `internal/console/assets/app.js`; verify browser lifecycle against the real binary.

## Cross-cutting completion
- [x] T010 Update README, error/OpenAPI contracts, `CODEBASE_REVIEW.md`, and browser CI workflow.
- [x] T011 Run focused/full Go tests, race tests, static build, vet, format and lint; record results in `specs/004-first-code-reliability/validation.md`.

Dependencies: T001 → T002; T003 → T004; T005 → T006; T002 → T007 → T008 → T009; all → T010 → T011. US1 and US2 tests are independent; execute locally in sequence to simplify review. No parallel implementation agents are needed. Each fix starts with a failing test; complete the whole authorized milestone rather than stopping at an MVP.
