# Security policy

## Reporting a vulnerability

Please do not open a public issue for a suspected security problem.

Report it privately through GitHub's "Report a vulnerability" feature on the
repository's **Security** tab (<https://github.com/utkayd/qurator/security/advisories/new>).
That creates a private advisory that only the maintainer can see.

Include what you can: the affected version or commit, steps to reproduce, and
the impact you believe it has. You will get an acknowledgement, and a fix or an
explanation will follow in the advisory thread before anything is disclosed
publicly.

## Scope

qurator is a self-hosted service. Reports about the qurator binary, its
embedded console, its Docker image, and the deployment files in `deploy/` are
in scope. Problems in third-party services you run alongside it (PostgreSQL,
MinIO, a reverse proxy) belong with those projects.
