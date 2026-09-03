# Feature Specification: qurator v1 — Self-Hostable QR Service

**Feature Branch**: `001-qr-service-baseline`

**Created**: 2026-09-04

**Status**: Draft

**Input**: User description: "qurator — an open-source, self-hostable QR code service. Baseline v1 specification covering the full product: ephemeral generation, dynamic QR codes with editable destinations and optional custom aliases, redirects, scan analytics, styling and branding, unified token-based authentication with a forward-auth escape hatch, a REST API, and a minimal embedded web console. Pluggable metadata and blob storage, embedded defaults, no required external services."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Generate a QR code instantly, storing nothing (Priority: P1)

A developer integrating qurator into their own product needs a QR image for a value they
already hold — an order number, a Wi-Fi credential, a URL. They send the content and get
back an image immediately. qurator keeps no record of it: no row, no file, no trace. They
can call this thousands of times without the instance accumulating anything.

**Why this priority**: This is the smallest slice that delivers standalone value, and it
is the only capability that works with no storage configured at all. It is the entry point
for most integrators and the foundation every other story renders through.

**Independent Test**: Start a fresh instance with no configuration, request a QR for a
known string, decode the returned image, and confirm the decoded value matches. Confirm
the datastore and blob store remain empty and that the instance functions with no
database or object storage configured.

**Acceptance Scenarios**:

1. **Given** a running instance with no storage configured, **When** a caller requests a QR
   image for the text "hello", **Then** an image is returned that decodes back to exactly
   "hello".
2. **Given** a caller requesting the same content twice, **When** both responses are
   compared, **Then** the images are byte-identical and no record of either request exists
   in any datastore.
3. **Given** a request specifying an output format, **When** the format is raster or
   vector, **Then** the response is returned in the requested format with a correct
   content type.
4. **Given** a request whose content exceeds the maximum encodable payload size,
   **When** it is submitted, **Then** it is rejected with a clear error naming the limit,
   and no partial image is returned.
5. **Given** the instance's default configuration, **When** an unauthenticated caller
   requests ephemeral generation, **Then** the request is rejected; **and when** the
   operator explicitly enables public generation, **Then** the same request succeeds and
   is subject to a rate limit.

---

### User Story 2 - Change where a printed QR code points (Priority: P2)

A marketer prints a QR code on 5,000 flyers pointing at a campaign landing page. Two weeks
later the campaign moves to a different URL. They edit the destination in qurator, and
every already-printed flyer now leads to the new page. Nothing is reprinted.

**Why this priority**: This is the reason the service is worth self-hosting rather than
generating QR images with a local library. It is the core differentiating capability, and
it establishes the persistence and redirect machinery all later stories build on.

**Independent Test**: Create a dynamic code pointing at destination A, scan it and confirm
arrival at A, change the destination to B, scan the same code again and confirm arrival at
B — with no change to the QR image itself.

**Acceptance Scenarios**:

1. **Given** a dynamic code created for destination A, **When** it is scanned, **Then** the
   scanner is redirected to A.
2. **Given** that same code, **When** its destination is updated to B, **Then** subsequent
   scans of the unchanged image redirect to B.
3. **Given** a dynamic code, **When** its stored image is retrieved by identifier at any
   later time, **Then** the same image is returned and it still decodes to the same
   qurator scan address.
4. **Given** a request to set a destination using a scheme outside the permitted set,
   **When** it is submitted, **Then** it is rejected and the existing destination is
   unchanged.
5. **Given** a code that has been deleted or disabled, **When** it is scanned, **Then** the
   scanner receives a clear, non-error-looking landing response rather than a broken page,
   and the scan is not silently redirected anywhere unexpected.
6. **Given** an owner listing their codes, **When** they have many, **Then** results are
   paginated and can be filtered by creation time and by destination.
7. **Given** an owner who supplies a custom alias, **When** the alias is unused, permitted,
   and not reserved, **Then** the code is reachable at that alias; **and when** the alias
   is already taken, differs only by letter case from a taken one, or is reserved, **Then**
   creation is refused and no code is created.

---

### User Story 3 - Secure the instance and issue revocable credentials (Priority: P3)

An operator exposes their qurator instance to the internet. They sign in, create a token
for their CI pipeline, and later revoke that token when the pipeline is retired. A second
operator already runs a company sign-on proxy and wants qurator to simply trust the
identity that proxy asserts, rather than maintaining a separate password.

**Why this priority**: No instance should be published without it, but the preceding
stories are demonstrable behind a private network first. Placing it here keeps the
credential model informed by the surfaces it must actually protect.

**Independent Test**: Sign in with the bootstrap account, mint a token, use it to
authenticate an API call, revoke it, and confirm the same call is refused immediately
without restarting the service. Separately, enable delegated identity, send a request
carrying an asserted identity from a trusted source and confirm it is honoured, then send
the same header from an untrusted source and confirm it is ignored.

**Acceptance Scenarios**:

1. **Given** a first start with bootstrap credentials supplied by configuration, **When**
   the service starts, **Then** a single administrative account exists and can sign in.
2. **Given** a signed-in browser session and a machine caller holding a token, **When**
   each calls the same protected operation, **Then** both are accepted through one
   consistent identity check.
3. **Given** a newly created token, **When** it is created, **Then** its secret value is
   displayed exactly once and is never retrievable again from any surface.
4. **Given** a revoked token, **When** it is presented, **Then** it is refused
   immediately, with no service restart required.
5. **Given** delegated identity is enabled with a list of trusted upstream sources,
   **When** an asserted identity arrives from a source not on that list, **Then** it is
   ignored entirely and the request is treated as unauthenticated.
6. **Given** delegated identity is not enabled, **When** any request carries an identity
   assertion header, **Then** the header has no effect whatsoever.
7. **Given** a configuration with no signing secret set and development mode not
   explicitly enabled, **When** the service starts, **Then** it refuses to start and
   explains why.

---

### User Story 4 - Understand how a code is performing (Priority: P4)

The marketer wants to know whether the flyer campaign worked: how many scans, when they
happened, on what kind of device, and from which referring source. They open the code's
analytics and see totals and a trend over time, without any individual being identifiable.

**Why this priority**: Analytics turn a redirect service into a campaign tool, but every
preceding story is fully useful without them. Deferring them also keeps the scan path
simple until its performance characteristics are established.

**Independent Test**: Issue a known number of scans against a code from varied device
types and referrers, then confirm the reported totals and breakdowns match the scans
issued, and confirm scan latency is unaffected when the analytics writer is deliberately
stalled.

**Acceptance Scenarios**:

1. **Given** a code scanned N times, **When** its analytics are viewed, **Then** the total
   scan count equals N.
2. **Given** scans spread across several days, **When** a time range is selected, **Then**
   only scans within that range are counted and a trend over time is shown.
3. **Given** scans from different device categories and referrers, **When** breakdowns are
   viewed, **Then** each dimension totals to the overall count for the same range.
4. **Given** the analytics writer is unavailable or saturated, **When** a code is scanned,
   **Then** the redirect completes at normal speed, the event is discarded, and the
   discard is counted in operational metrics.
5. **Given** any configuration, **When** scan records are inspected directly in storage,
   **Then** no full network address of any scanner is present, and no geographic attribute
   is recorded.
6. **Given** a configured retention period has elapsed, **When** retention runs, **Then**
   individual scan records older than the period are removed while historical aggregate
   totals remain available.

---

### User Story 5 - Make the code look like it belongs to the brand (Priority: P5)

A designer needs the QR on the packaging to match the product's palette and carry the
company mark in the centre. They set colours, module shape, and a centre logo, preview the
result, and are warned before shipping a combination that scanners will struggle with.

**Why this priority**: Branding drives adoption over plain generators but is cosmetic
relative to correctness. It comes after the functional core so that decodability
verification has an established generation path to test against.

**Independent Test**: Render codes across the full range of supported styling options,
decode every rendered output, and confirm each still decodes to the original content;
confirm low-contrast and oversized-logo combinations are refused or flagged.

**Acceptance Scenarios**:

1. **Given** custom foreground and background colours, **When** a code is rendered,
   **Then** the output uses those colours and still decodes to the original content.
2. **Given** a colour pair whose contrast falls below the scannable threshold, **When**
   rendering is requested, **Then** the request is rejected with an explanation rather
   than producing an unscannable image.
3. **Given** a centre logo, **When** it is applied, **Then** error correction is raised
   automatically as needed and the result still decodes.
4. **Given** a logo larger than the recoverable proportion of the symbol, **When** it is
   submitted, **Then** it is rejected with an explanation of the maximum.
5. **Given** any supported combination of module shape, margin, size, and error correction
   level, **When** rendered, **Then** the output decodes to the original content.
6. **Given** a styling profile applied to a dynamic code, **When** the code's destination
   is later changed, **Then** the stored image and its styling are unchanged.

---

### User Story 6 - Manage everything without writing a request by hand (Priority: P6)

A non-technical user opens qurator in a browser, types a destination, adjusts colours while
watching the preview update, saves the code, downloads the image, and later returns to
check its scans — never touching a terminal.

**Why this priority**: The console makes the product usable by the audience that most needs
it, but it is a presentation layer over capabilities the earlier stories already deliver
and prove.

**Independent Test**: Complete the entire lifecycle — sign in, create a styled dynamic
code, download it, edit its destination, view its analytics, create and revoke a token —
using only the browser interface, on a fresh instance.

**Acceptance Scenarios**:

1. **Given** a signed-in user in the console, **When** they adjust any styling control,
   **Then** the preview reflects the change without saving anything.
2. **Given** a completed form, **When** they save, **Then** a dynamic code is created and
   appears in their list.
3. **Given** a code in the list, **When** they open it, **Then** they can download the
   image, edit the destination, view analytics, and delete the code.
4. **Given** the console is served, **When** the instance runs with no network access to
   any external origin, **Then** the interface loads and functions completely.
5. **Given** a destructive action such as deletion, **When** it is chosen, **Then**
   confirmation is required and the consequence for already-printed codes is stated.

---

### User Story 7 - Run it seriously, and leave when you want (Priority: P7)

An operator moves from evaluating qurator on a laptop to running it for their organisation:
they point it at an external database and object store, watch its metrics, and satisfy
themselves they can export everything and walk away.

**Why this priority**: These qualities must be designed in from the start, but they are
verified last, once there is a complete system to migrate, observe, and export.

**Independent Test**: Run the full acceptance suite twice — once on embedded defaults and
once against external database and object storage — and confirm identical behaviour; then
export all data and confirm the export contains every code, destination, and aggregate.

**Acceptance Scenarios**:

1. **Given** a fresh install with no configuration, **When** it starts, **Then** it serves
   requests successfully with no external service running.
2. **Given** configuration pointing at an external database and object store, **When** the
   same acceptance suite runs, **Then** every behaviour is identical to the embedded
   default run.
3. **Given** a running instance, **When** liveness and readiness are queried, **Then**
   liveness succeeds independently of storage health while readiness reflects the health
   of configured storage.
4. **Given** a shutdown signal, **When** it is received, **Then** in-flight requests
   complete, buffered analytics are flushed, and the process exits cleanly.
5. **Given** an operator running the export, **When** it completes, **Then** it contains
   every code, destination, styling profile, and scan aggregate in a documented,
   machine-readable format.
6. **Given** an instance running with default configuration, **When** its outbound network
   traffic is observed, **Then** it contacts no external service of the project's.

---

### Edge Cases

- **Short code collision**: two codes generated at the same instant produce the same short
  code — creation must retry and never overwrite or alias an existing code.
- **Alias squatting or shadowing**: a user claims an alias matching an instance route, an
  administrative path, or the alias of a recently deleted campaign — must be refused by the
  reserved-word list and the retired-alias rule, so a printed code can never be captured by
  a later registration.
- **Alias case and homoglyph confusion**: two aliases differing only by letter case, or by
  visually similar characters — uniqueness must be enforced case-insensitively over a
  restricted character set.
- **Destination loop**: a destination points back at the instance's own scan address,
  directly or via a chain — must be detected and refused rather than creating a redirect
  loop.
- **Hostile destination**: a destination uses a scheme capable of executing in a scanner's
  browser, or is changed to a hostile target after creation — only permitted schemes are
  ever accepted, on create and on every update.
- **Scan of a nonexistent code**: an unknown or malformed short code is scanned — must
  produce a clear landing response, not a stack trace or a generic server error, and must
  not be expensive enough to serve as an enumeration or amplification vector.
- **Scan burst**: a single code is scanned far faster than analytics can be written — the
  redirect path must not slow down, and events beyond capacity are dropped and counted.
- **Blob store unavailable**: object storage is down when a stored image is requested —
  scan redirects must continue working, since they do not depend on the image.
- **Storage full or read-only**: the datastore rejects writes — creation fails with a clear
  error while existing codes continue to redirect.
- **Oversized or malicious render request**: enormous dimensions, a huge logo, or a payload
  crafted to maximise render cost — rendering must be bounded in size and time.
- **Clock skew or expired credential**: a token presented after expiry, or with a
  timestamp from the future — must be refused consistently.
- **Duplicate identity assertion**: a request carries multiple conflicting identity
  headers — must be refused rather than resolved by picking one.
- **Concurrent destination edits**: two users edit the same code simultaneously — the
  outcome must be deterministic and the losing write must not be silently discarded.
- **Unicode and binary payloads**: content containing emoji, right-to-left text, or raw
  bytes — must round-trip exactly through encoding and decoding.

## Requirements *(mandatory)*

### Functional Requirements

**Ephemeral generation**

- **FR-001**: System MUST accept a generation request carrying content and optional
  styling and return the rendered QR image in the response, without creating any record in
  the metadata store or blob store.
- **FR-002**: System MUST support both raster and vector output formats and MUST label
  each response with the correct content type.
- **FR-003**: System MUST perform ephemeral generation successfully when no metadata store
  and no blob store are configured or reachable.
- **FR-004**: System MUST produce byte-identical output for identical input, so responses
  are cacheable by intermediaries.
- **FR-005**: System MUST enforce a configurable maximum content length and reject
  oversized payloads with an error naming the limit.
- **FR-006**: System MUST require a valid credential for ephemeral generation by default,
  and MUST provide an explicit opt-in setting that permits unauthenticated access subject
  to a configurable rate limit.

**Dynamic codes and redirects**

- **FR-007**: Users MUST be able to create a dynamic code with a destination, receiving a
  short code and a scannable image whose encoded value is the instance's scan address for
  that short code.
- **FR-008**: Users MUST be able to list, read, update the destination of, disable, and
  delete their dynamic codes.
- **FR-009**: System MUST resolve a scan of a short code to that code's current destination
  and issue a redirect, without requiring any credential.
- **FR-010**: System MUST NOT alter the encoded value or stored image of a code when its
  destination changes.
- **FR-011**: System MUST validate every destination against a configurable allow-list of
  permitted schemes, on creation and on every update, defaulting to web schemes only.
- **FR-012**: System MUST detect and refuse destinations that resolve back to the
  instance's own scan path.
- **FR-013**: System MUST generate short codes that are unguessable, collision-resistant,
  and safely usable in a URL, and MUST retry rather than overwrite on collision.
- **FR-014**: System MUST return a clear, human-readable landing response for scans of
  unknown, disabled, or deleted codes, and MUST support an operator-configured fallback
  destination for this case.
- **FR-015**: System MUST persist each dynamic code's rendered image in the blob store and
  serve it by identifier, with cache validation headers, without requiring a credential.
- **FR-016**: System MUST paginate list results and support filtering by creation time and
  by destination.
- **FR-017**: System MUST resolve a scan using at most one metadata lookup, and MUST serve
  repeat scans of the same code from an in-process cache with a bounded staleness window.
- **FR-018**: Users MUST be able to supply a custom alias in place of a generated short
  code when creating a dynamic code. The system MUST:
  - accept an alias only if it is unique across all codes, comparing case-insensitively;
  - restrict aliases to a documented character set safe in a URL, with a documented
    minimum and maximum length;
  - refuse any alias appearing on a reserved-word list covering the instance's own route
    names and administrative paths;
  - treat an alias as immutable once created, so a printed code can never be repointed by
    reassigning its alias to a different code;
  - refuse an alias previously used by a deleted code, unless an administrator explicitly
    releases it, so retired codes cannot be silently hijacked.

**Analytics**

- **FR-019**: System MUST record a scan event for every successful redirect, capturing
  timestamp, user agent family, device category, and referrer.
- **FR-020**: System MUST record scan events asynchronously, off the request path, so that
  a slow, failing, or saturated analytics writer never delays or fails a redirect.
- **FR-021**: System MUST discard scan events when its buffer is full and MUST expose a
  counter of discarded events.
- **FR-022**: System MUST NOT retain the full network address of a scanner, in any
  configuration. Addresses may be used transiently within a request — for rate limiting or
  abuse control — and MUST NOT be written to any durable store.
- **FR-023**: Users MUST be able to view, for a code and a chosen time range, the total
  scan count, a trend over time, and a breakdown by each recorded dimension.
- **FR-024**: System MUST enforce a configurable retention period for individual scan
  events, and MUST preserve aggregate totals beyond that period.
- **FR-025**: System MUST NOT perform geographic attribution of scans in v1, and MUST NOT
  acquire, embed, or fetch any address-to-location dataset. Geography is deliberately
  excluded because every means of deriving it would introduce a mandatory data dependency
  or a mandatory upstream proxy, either of which breaks the promise that an instance runs
  with no external services. The scan event and aggregate models MUST NOT be shaped in a
  way that precludes adding a geographic dimension later.

**Styling**

- **FR-026**: Users MUST be able to specify foreground colour, background colour, module
  shape, quiet-zone margin, output dimensions, and error correction level, for both
  ephemeral and dynamic codes.
- **FR-027**: Users MUST be able to supply a centre logo image, and the system MUST raise
  the error correction level automatically as needed to preserve decodability.
- **FR-028**: System MUST reject styling combinations that render the code unscannable —
  including insufficient contrast and a logo exceeding the recoverable area — with an
  explanation identifying the offending option.
- **FR-029**: System MUST bound rendering by maximum output dimensions and a maximum
  rendering duration, rejecting requests that would exceed either.
- **FR-030**: System MUST store a dynamic code's styling with the code, so the image can be
  re-rendered identically.

**Identity and access**

- **FR-031**: System MUST verify browser and machine callers through a single credential
  verification path, accepting a credential presented either as a request header or as a
  session cookie restricted to the instance.
- **FR-032**: System MUST create exactly one administrative account on first start from
  configured bootstrap values, and MUST NOT recreate or reset it on subsequent starts.
- **FR-033**: Users MUST be able to create named API tokens, and the system MUST display a
  token's secret exactly once at creation and never again.
- **FR-034**: System MUST store token secrets only as irreversible hashes.
- **FR-035**: Users MUST be able to revoke a token, taking effect immediately without a
  restart.
- **FR-036**: System MUST support a delegated identity mode in which it accepts a
  configurable identity header asserted by an upstream proxy, and this mode MUST be
  disabled by default.
- **FR-037**: System MUST, when delegated identity is enabled, accept the assertion only
  from a configured list of trusted upstream sources, and MUST ignore it otherwise.
- **FR-038**: System MUST refuse a request carrying conflicting identity assertions.
- **FR-039**: System MUST NOT provide account federation, self-service registration, or
  password reset flows.
- **FR-040**: System MUST refuse to start when no credential signing secret is configured
  and development mode has not been explicitly enabled.

**Interfaces**

- **FR-041**: System MUST expose a versioned programmatic interface covering every
  capability in this specification.
- **FR-042**: System MUST serve a browser console supporting sign-in, code creation with
  live styling preview, code listing and management, per-code analytics, and token
  management.
- **FR-043**: System MUST serve the console entirely from the instance itself, functioning
  with no access to any external origin.
- **FR-044**: System MUST return errors in a consistent, machine-readable shape carrying a
  stable code and a human-readable message, and MUST NOT leak internal detail.

**Operability and portability**

- **FR-045**: System MUST start and serve every capability with no external service
  configured, using an embedded datastore and local disk for images.
- **FR-046**: System MUST support switching the metadata store to an external relational
  database and the blob store to S3-compatible object storage, by configuration alone,
  with no behavioural difference.
- **FR-047**: System MUST read all configuration from the environment, with precedence
  flags over environment over file over default.
- **FR-048**: System MUST default every exposure-widening setting to off.
- **FR-049**: System MUST NOT log, echo, or return any configured secret.
- **FR-050**: System MUST expose distinct liveness and readiness signals, where liveness
  does not depend on storage health.
- **FR-051**: System MUST emit operational metrics covering request rate, latency, and
  error rate per route, plus counters for generation, scans, and discarded scan events.
- **FR-052**: System MUST emit structured logs carrying a correlation identifier that
  spans a request.
- **FR-053**: System MUST, on a shutdown signal, stop accepting new work, complete
  in-flight requests, flush buffered scan events, and exit cleanly.
- **FR-054**: System MUST apply schema migrations automatically at startup for either
  supported relational backend, from one shared migration set.
- **FR-055**: System MUST provide a documented, scriptable export of all codes,
  destinations, styling, and scan aggregates in a machine-readable format.
- **FR-056**: System MUST NOT transmit any telemetry, usage report, or version check to any
  service operated by the project.

### Key Entities

- **User**: An identity that owns codes and tokens. Attributes: identifier, email address,
  credential material for local accounts, creation time, administrative flag. A user owns
  many codes and many tokens.
- **API Token**: A revocable machine credential belonging to a user. Attributes:
  identifier, owner, human-readable name, irreversible hash of the secret, creation time,
  last-used time, revocation time. The secret itself exists only at the moment of creation.
- **Dynamic Code**: A persisted, scannable code with a mutable destination. Attributes:
  short code — system-generated or a user-supplied alias, unique case-insensitively and
  immutable once set — owner, current destination, permitted state (active or disabled),
  styling profile, blob reference for its rendered image, creation and update times. Owns
  many scan events.
- **Styling Profile**: The visual configuration of a rendered code. Attributes: foreground
  and background colour, module shape, margin, dimensions, error correction level, optional
  logo reference. Attached to a dynamic code, or supplied inline for ephemeral generation.
- **Scan Event**: A single recorded scan. Attributes: code reference, timestamp, user
  agent family, device category, referrer. Deliberately excludes any full network address
  and any geographic attribute. Subject to a retention period.
- **Scan Aggregate**: Rolled-up scan totals per code and time bucket, retained beyond
  individual scan events so long-range trends survive retention pruning.
- **Instance Configuration**: The operator's chosen settings — storage backends,
  credential and delegated identity settings, exposure toggles, limits, retention. Read
  from the environment, never persisted through the interfaces.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A new operator goes from nothing to a working instance serving its first QR
  code in under 5 minutes, using one command and no configuration file.
- **SC-002**: An instance with no external services configured starts and serves every
  capability in this specification, with zero required dependencies beyond the running
  process.
- **SC-003**: 99% of scans of a known code complete their redirect in under 50 milliseconds
  at the instance, measured excluding network transit.
- **SC-004**: A single modest instance sustains 1,000 scans per second without redirect
  latency rising above the SC-003 threshold.
- **SC-005**: Redirect latency remains within the SC-003 threshold while the analytics
  writer is entirely stalled, and every discarded event is accounted for in a counter.
- **SC-006**: Ephemeral generation of a typical payload completes in under 20 milliseconds
  at the instance, and sustains 500 generations per second on a single instance.
- **SC-007**: 100% of rendered outputs, across every supported combination of styling
  options, decode back to their original content when verified by an independent decoder.
- **SC-008**: A marketer changes a printed code's destination and observes the new
  destination on the next scan within 60 seconds, without reprinting anything.
- **SC-009**: A non-technical user completes the full lifecycle — create a styled code,
  download it, change its destination, read its analytics — using only the browser, with no
  documentation, on the first attempt.
- **SC-010**: The identical acceptance suite passes unchanged against both the embedded
  default storage and external database and object storage, with zero behavioural
  differences.
- **SC-011**: A revoked credential is refused on its next use, with no service restart and
  no more than 60 seconds of propagation delay.
- **SC-012**: No stored scan record contains a full network address or a geographic
  attribute, in any configuration, verified by direct inspection of storage.
- **SC-013**: An operator exports all data and re-imports it into a fresh instance, and
  every code, destination, styling profile, and scan aggregate is present and identical.
- **SC-014**: A shutdown signal results in zero dropped in-flight requests and zero
  unflushed buffered scan events.
- **SC-015**: The distributed artefact runs on both common processor architectures as an
  unprivileged user, verified by an automated release check.

## Assumptions

- **Storage defaults**: An embedded file-based datastore and the local filesystem are
  acceptable defaults for evaluation and small deployments; external database and object
  storage are the documented path for larger ones. Chosen to honour the constitution's
  self-hostability principle.
- **Single tenancy**: Every code belongs to exactly one user, and there is no organisation,
  team, or sharing layer. Multi-tenancy is explicitly deferred, though the data model
  should not preclude adding it.
- **Bootstrap account**: One administrative account seeded from configuration is sufficient
  for v1; additional users, if any, are created by that administrator or arrive via
  delegated identity. Self-service registration is out of scope.
- **Delegated identity covers SSO**: Operators needing OIDC, SAML, or corporate sign-on
  will run an existing authentication proxy in front of qurator. qurator implements no
  identity provider of its own.
- **Permitted destination schemes**: Default to web schemes only. Operators who need
  others opt in explicitly, accepting the risk.
- **Redirect semantics**: Scans use a non-permanently-cached redirect so that destination
  changes take effect promptly, accepting the cost of not being cached indefinitely by
  intermediaries. This is what makes SC-008 achievable.
- **Analytics privacy**: Scanner network addresses are never persisted in any
  configuration; the default retention period for individual scan events is one year, with
  aggregates retained indefinitely.
- **No geography in v1**: Geographic breakdown is deliberately excluded. Every way to
  obtain it — an embedded lookup dataset, a downloaded one, or a mandatory upstream proxy
  supplying a country header — would make an external dependency compulsory and contradict
  the self-hostability principle. The data model leaves room to add it in a later version
  once an operator-opt-in mechanism is designed.
- **Custom aliases**: Aliases are available to any user, since v1 is single-tenant and the
  owner is normally the operator. If multi-tenancy arrives, alias allocation will need a
  policy; the immutability and retired-alias rules exist so that decision does not
  invalidate codes already printed.
- **Scale envelope**: Targets a single instance serving up to roughly ten million scans per
  month. Horizontal scaling and sharding are out of scope for v1.
- **Image immutability**: A dynamic code's rendered image is generated once at creation.
  Restyling an existing code is treated as creating a new code, not mutating a printed one.
- **Console scope**: The browser console covers the common lifecycle, not every capability
  of the programmatic interface. Advanced operations are expected to be scripted.
- **Out of scope for v1**: geographic scan analytics; multi-tenancy and teams; a
  self-contained identity provider; a standalone command-line binary; a published library
  surface for other projects; bulk or batch generation; per-code custom domains; password
  reset flows; and QR formats beyond the standard symbology.
