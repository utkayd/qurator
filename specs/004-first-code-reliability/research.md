# Research decisions

- **Origin**: a small domain parser is shared by startup config and service construction; it imports URL/network utilities, no HTTP/storage. Empty means unconfigured. Reject prefixes because all current routes mount at `/`; never infer origin from Host. This corrects FR-007 without breaking zero-config startup.
- **HTMX**: local vendored source defaults 4xx/5xx to no swap and disables swapped scripts in app configuration. Mark expected HTML error fragments with a console-specific response header; swap only those into the stable error slot, keeping inputs. Use document listeners and idempotent initialization on `htmx:load`. Handle unexpected responses/network failures with generic text.
- **Clipboard**: retain selectable secret on rejection/unavailability; only successful promise completion may hide it. Browser fixtures simulate browser API outcomes without bypassing real application events.
- **Browser tooling**: independent local research found Node 24/npm 11 and installed Chrome, no cached Playwright. Use private pinned Playwright test dependency, isolated fixtures and CI Chromium. Official [configuration](https://playwright.dev/docs/test-configuration) and [web server guidance](https://playwright.dev/docs/test-webserver) support running tests against a managed real server. No frontend bundler is added.
- **Sign-in**: share one limiter instance across API and console route dispatch, not two separate quotas. Keep native sign-in form behavior and return safe HTML for the console's 429. No forwarded-address trust change.

Optional agent-context hooks are satisfied by updating the current plan reference in CLAUDE.md; no mandatory hooks are installed.
