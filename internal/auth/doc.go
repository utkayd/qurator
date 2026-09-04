// Package auth is the single credential-verification path (FR-031): API tokens presented
// as a Bearer header, HS256 session cookies, and — when enabled and only from a trusted
// TCP peer — a forward-auth identity header. See research.md §2 for every decision here.
//
// Credential types and why they hash differently:
//   - API tokens are 256-bit CSPRNG secrets, stored as SHA-256 (or HMAC-SHA256 with a
//     pepper). A slow KDF adds nothing to a random secret and would put a CPU-exhaustion
//     vector on the hot path.
//   - The human password uses Argon2id (RFC 9106 lighter profile), because it is
//     low-entropy.
//
// Token wire format: "qur_" + base64url(32 random bytes) = 47 characters. The Store
// interface only offers GetTokenByID, so the first 10 of those 32 bytes double as the
// token ID material ("tok_" + base32 of them); the remaining 22 bytes (176 bits) are the
// secret proper. Lookup is: decode → derive ID → GetTokenByID → constant-time compare
// of SHA-256(whole wire string) with the stored hash.
package auth
