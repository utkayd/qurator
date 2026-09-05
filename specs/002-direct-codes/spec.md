# Feature Specification: Direct Codes — persisted QR codes that bypass redirection

**Feature Branch**: `002-direct-codes`

**Created**: 2026-09-05

**Status**: Draft

**Input**: User description: "We should have an option to send directly instead of tracking — the QR code directly encodes a URL instead of passing through our redirection. Redirection is core, but it needs to be bypassable if desired."

## Motivation

v1 offers two modes: **ephemeral** (encode anything, store nothing) and **dynamic** (a
short code that redirects, so the destination can change and scans are counted). There is
no way to keep a *managed* record — listed, downloadable, restylable, deletable — of a code
whose printed image goes straight to its destination. Some operators do not want their
service in the scan path at all: a QR on a product that must keep working if the qurator
instance is ever decommissioned, a code pointing at a third-party service that does its
own tracking, or a privacy posture where the instance must not observe scans.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Create a code that scans straight to its destination (Priority: P1)

An operator creates a code in **direct** mode. The image they download encodes the
destination URL itself. Scanning it opens the destination with no request to qurator.
The code still appears in their list with its styling, can be downloaded again later, and
can be deleted.

**Why this priority**: This is the feature. Everything else is consequence handling.

**Independent Test**: Create a direct code, decode the downloaded image with the
independent decoder, and assert the decoded value is the destination URL — not a qurator
address. Then stop the qurator instance and confirm the decoded URL still resolves.

**Acceptance Scenarios**:

1. **Given** a create request with `mode: direct` and a destination, **When** it succeeds,
   **Then** the stored image decodes to exactly the destination URL.
2. **Given** a direct code, **When** the instance is unreachable, **Then** the printed code
   still works, because nothing about it depends on the instance.
3. **Given** a direct code, **When** listed or read, **Then** it is clearly marked as
   direct, and the response carries no scan address.
4. **Given** no `mode` in a create request, **When** it succeeds, **Then** the code is
   dynamic — the existing behaviour is unchanged.
5. **Given** a direct code, **When** its styling is chosen, **Then** every styling option
   available to dynamic codes applies identically.

---

### User Story 2 - The immutability consequence is unmistakable (Priority: P2)

The operator later tries to change a direct code's destination and is told plainly that it
cannot be changed because it is printed into the image, and that a new code is the answer.

**Why this priority**: Silent failure here would send someone to reprint 5,000 flyers
believing they had fixed them.

**Independent Test**: Attempt to update the destination of a direct code and assert a
refusal whose error code and message name the reason.

**Acceptance Scenarios**:

1. **Given** a direct code, **When** its destination update is requested, **Then** the
   request is refused with a distinct, stable error code and a message stating the
   destination is encoded in the image.
2. **Given** a direct code, **When** disable or enable is requested, **Then** it is
   refused the same way: those states only have meaning on the redirect path.
3. **Given** the console's create form, **When** direct mode is selected, **Then** the
   form states before saving that the destination cannot be changed afterwards and that
   scans will not be counted.

---

### User Story 3 - The analytics consequence is honest (Priority: P3)

The operator opens analytics for a direct code and sees an explanation rather than a
zero that looks like "nobody scanned it".

**Why this priority**: An empty chart is indistinguishable from a failed campaign.

**Independent Test**: Request analytics for a direct code and assert the response is a
clear "not tracked" indication, not an empty result.

**Acceptance Scenarios**:

1. **Given** a direct code, **When** its analytics are requested through the API, **Then**
   the response indicates scans are not tracked for direct codes, with a stable code.
2. **Given** a direct code, **When** its detail page is opened in the console, **Then** the
   analytics section explains that scans go straight to the destination and are not
   observed, instead of showing a chart.

---

### Edge Cases

- **Destination longer than a QR can hold**: a direct code encodes the full URL, so a very
  long destination can exceed capacity at the chosen error-correction level. It must be
  rejected at creation with the limit named, exactly as ephemeral generation does. Dynamic
  codes are unaffected because they encode a short address.
- **Scan of a direct code's short address**: direct codes still receive a short code for
  identity and image addressing. If someone requests `/r/{code}` for a direct code, the
  service redirects to the destination and records the scan like any other — this is a
  link click, not a QR scan, and the printed image never produces it.
- **Mode is immutable**: a code cannot be converted between direct and dynamic. The image
  is what it is; conversion would be a new code.
- **Existing codes**: every code created before this feature is dynamic. Migration must
  make that explicit, not implicit.
- **Export/import**: mode round-trips.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-101**: System MUST accept a `mode` on code creation with values `dynamic` (default)
  and `direct`, and MUST persist it immutably with the code.
- **FR-102**: For a direct code, System MUST encode the destination URL itself into the
  rendered image; for a dynamic code, System MUST encode the instance's scan address, as
  today.
- **FR-103**: System MUST validate a direct code's destination against the same scheme
  allow-list and self-reference rules as a dynamic code, AND against the encodable
  capacity for the chosen error-correction level, rejecting with `content_too_large` when
  it does not fit.
- **FR-104**: System MUST refuse destination updates, disable, and enable on a direct code
  with a distinct stable error code (`direct_code_immutable`) whose message states the
  destination is encoded in the image.
- **FR-105**: System MUST return, for analytics requests on a direct code, a stable
  indication that scans are not tracked (`not_tracked`), never an empty aggregate.
- **FR-106**: Every code representation (create, read, list, export) MUST include `mode`,
  and MUST omit the scan address for direct codes.
- **FR-107**: The console MUST offer the mode choice at creation, MUST state the two
  consequences (immutable destination, no scan analytics) before saving, and MUST replace
  the analytics section with an explanation on a direct code's detail page.
- **FR-108**: All existing codes MUST be recorded as `dynamic` by the migration that adds
  the mode.
- **FR-109**: A direct code MUST still be listable, readable, restylable-by-recreation,
  downloadable, and deletable exactly as a dynamic code; its short code remains reserved
  after deletion (FR-018 applies unchanged).

### Key Entities

- **Code** gains `mode` (`dynamic` | `direct`), immutable. The rendered image's encoded
  content becomes a function of mode: scan address for dynamic, destination for direct.

## Success Criteria *(mandatory)*

- **SC-101**: A direct code's downloaded image decodes to its destination URL with the
  independent decoder, on both PNG and SVG, across every styling option.
- **SC-102**: With the instance stopped, a direct code still resolves — verified by
  decoding the image and fetching the decoded URL with no qurator process running.
- **SC-103**: 100% of destination-update, disable, and enable attempts on direct codes
  are refused with `direct_code_immutable`.
- **SC-104**: Creating a code with no mode produces behaviour byte-identical to v1
  (same encoded content, same responses).
- **SC-105**: The backend-parity matrix passes unchanged with direct codes added to the
  lifecycle sequence.

## Assumptions

- **Direct codes keep a short code.** It gives the code an identity, an image address, and
  a working short link, and it keeps one table with one uniqueness rule. The printed QR
  never encodes it. The alternative — a separate table for direct codes — duplicates
  listing, styling, and deletion logic for no user-visible gain.
- **Analytics for direct codes are not fabricated from short-link clicks.** `/r/{code}`
  on a direct code records a scan like any other, but the analytics endpoint reports
  `not_tracked` for direct codes rather than showing those clicks as if they were QR
  scans. Showing them would recreate exactly the misleading zero-or-not-zero this feature
  exists to avoid. If short-link click counts become wanted, they should be a separately
  named metric.
- **Ephemeral generation is unchanged.** It already is "direct without persistence".
- **Out of scope**: converting a code between modes; per-code opt-out of the landing page;
  a "direct" flavour of alias-only codes without an image.
