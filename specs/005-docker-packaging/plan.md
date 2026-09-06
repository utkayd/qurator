# Docker packaging plan

Baseline research §6 remains the design: Go multi-stage static build, pinned
distroless runtime, numeric nonroot user, built-in healthcheck, durable `/data`.
Constitution check: single binary and zero-service default retained; no API or
storage schema changes; real-image smoke coverage added for both architectures.

Observed regression before changes: `docker build` fails because BASE_DIGEST is
stage-local and unavailable to the final FROM. Move it before the first FROM.
Create an owned data directory in the image so fresh volumes inherit permissions.
Copy only Go module and application sources into the builder to exclude browser
dependencies, local binaries and development artifacts.

Add an isolated Docker smoke harness using Python's standard library, exercised
on pull requests and reused after release publication. It removes only its own
temporary container and volume. Build and smoke-test locally; document evidence.
