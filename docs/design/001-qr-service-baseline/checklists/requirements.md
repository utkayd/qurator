# Specification Quality Checklist: qurator v1 — Self-Hostable QR Service

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-09-04
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

**Iteration 2 (2026-09-04) — all items pass.** Both open questions were resolved by the
project owner and written back into the spec.

- **FR-018 (custom short-code aliases)** — RESOLVED: aliases are in scope, with a reserved
  word list. FR-018 now also fixes uniqueness as case-insensitive, aliases as immutable
  once created, and aliases of deleted codes as not silently reusable — the three rules
  that stop a printed code from ever being captured by a later registration. Matching
  acceptance scenario, edge cases, and entity attributes were added.

- **FR-025 (geographic attribution)** — RESOLVED: geography is dropped from v1. This
  removed a real conflict with Constitution Principle I, since every source of geographic
  data — an embedded dataset, a downloaded one, or a required upstream proxy — would have
  made an external dependency compulsory. FR-025 now records the exclusion and its
  rationale as an explicit MUST NOT, while requiring the data model to leave room for the
  dimension later. FR-022 was strengthened at the same time: scanner addresses are now
  never persisted in *any* configuration, not merely by default.

- **"No implementation details" — PASS, with one deliberate exception.** FR-046 and the
  assumptions name *S3-compatible object storage* and *relational database*. These are
  retained intentionally: interoperating with the S3 protocol is an operator-facing
  product promise about what infrastructure qurator can be pointed at, not a choice of
  library or framework. No language, framework, or package is named anywhere in the spec.

**Status: spec is ready for `/speckit-plan`.** `/speckit-clarify` is optional here, since
the two questions it would have surfaced were already asked and answered.
