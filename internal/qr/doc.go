// Package qr encodes content into a QR symbol and renders it as PNG or SVG.
//
// It is the ephemeral generation path (Constitution Principle III): it MUST NOT import
// internal/store or internal/blob, and nothing in it knows about persistence. tests/arch
// and .golangci.yml enforce the boundary.
//
// Design (research.md §1):
//   - piglig/go-qr encodes; EncodeSegments is called with boostEcl=false so the
//     requested error-correction level is exactly the one encoded.
//   - Module geometry is computed in-house once per symbol and consumed by both the PNG
//     rasteriser and the SVG builder, so the two formats cannot drift apart.
//   - Every render is bounded by Bounds (max pixels, max duration, max payload) and every
//     rejection is a typed error the HTTP layer maps to a stable error code.
//   - Output is deterministic: identical Options produce byte-identical bytes.
package qr
