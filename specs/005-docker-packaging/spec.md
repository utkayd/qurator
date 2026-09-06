# Docker packaging

Status: Complete (local builds and smoke checks passed; hosted CI awaits a push)

User request: deliver Qurator as a Docker image.

This completes baseline T098/T101 and SC-015, under Constitution I and VIII.
The image must contain the static binary and embedded console, run as nonroot on
linux/amd64 and linux/arm64, and require no external database or object service.
A fresh named `/data` volume must be writable by the runtime user. Replacing a
container with the same volume must preserve accounts, signing keys, code metadata
and QR blobs. The binary healthcheck must work without a shell or curl.

Acceptance: build the real Dockerfile; assert runtime UID, readiness and liveness;
sign in, create a QR image, replace the container without bootstrap credentials,
then use the original session to retrieve the same record and identical image.
Run these checks in CI for both release architectures. Document local build/run
and retain version-tag publication to GHCR. Publishing a release is outside this
local packaging task.
