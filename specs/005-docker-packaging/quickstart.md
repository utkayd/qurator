# Validate the Docker image

From the repository root:

```sh
docker build -f deploy/Dockerfile -t qurator:local .
python3 tests/docker/smoke.py qurator:local
```

The smoke test uses Docker and Python 3 only. It creates a random container and
named volume, verifies the built-in healthcheck and embedded console, creates an
authenticated dynamic code, then replaces the container using the same volume.
It checks the original session, database record, identical PNG blob and public
302/no-store redirect. It cleans up its own test storage even on failure.

For another supported architecture (requires Docker emulation on a different host):

```sh
docker build --platform linux/amd64 -f deploy/Dockerfile -t qurator:local-amd64 .
python3 tests/docker/smoke.py qurator:local-amd64 linux/amd64
```

See the README container command for persistent use with your own bootstrap
credentials and public scan origin. Keep `/data` across upgrades.
