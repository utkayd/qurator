package blob

import (
	"path"
	"strings"
)

// ValidateKey rejects keys that could escape a filesystem root or confuse an object
// store. Both drivers call it so they behave identically (contract requirement 6).
func ValidateKey(key string) error {
	switch {
	case key == "",
		len(key) > 512,
		strings.ContainsRune(key, 0),
		strings.HasPrefix(key, "/"),
		strings.HasPrefix(key, "\\"),
		strings.Contains(key, "\\"),
		strings.Contains(key, "//"):
		return ErrInvalidKey
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." {
			return ErrInvalidKey
		}
	}
	if path.Clean(key) != key {
		return ErrInvalidKey
	}
	return nil
}
