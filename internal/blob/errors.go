package blob

import "errors"

// Sentinel errors; drivers translate native errors into these (Principle II).
var (
	ErrBlobNotFound  = errors.New("blob: not found")
	ErrInvalidKey    = errors.New("blob: invalid key")
	ErrUnknownDriver = errors.New("blob: unknown driver")
)
