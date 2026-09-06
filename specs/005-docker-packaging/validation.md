# Docker validation — 2026-09-06

- Before fix: Docker build failed because final FROM expanded BASE_DIGEST to empty.
- `docker build -f deploy/Dockerfile -t qurator:local .`: passed, linux/arm64,
  image `sha256:b1cf18d8aadc42412ce25a7e45982f878600c97cfc7e90893444942b5d94191f`,
  26,805,527 bytes.
- `docker build --platform linux/amd64 -f deploy/Dockerfile -t qurator:local-amd64 .`:
  passed, image `sha256:0123b2d3776de007b18bd7513d117a5955c947f7096f8b6a759d573a65ed6a53`,
  28,488,992 bytes.
- Final `tests/docker/smoke.py` passed on both images (AMD64 via Docker Desktop
  emulation): UID/GID 65532, read-only root, healthy readiness, explicit liveness,
  embedded sign-in page, real bootstrap authentication, dynamic QR PNG creation,
  replacement container with persisted user/session/key/record/blob, public 302
  redirect to the destination and no-store header.
- `go test ./cmd/qurator`: passed, including healthcheck subcommand tests.
- Docker and release workflow YAML parsed successfully; `git diff --check` passed.

The temporary smoke containers and volumes were removed; both local image tags
remain available. No image was pushed and no release was tagged. Hosted CI and
registry publication have not run in this session. Image rendering here is checked
as a PNG and for persisted equality; independent QR decoding is covered by the
existing Go/browser suites recorded in feature 004, not this packaging smoke test.
