package qr

import (
	"errors"
	"fmt"

	goqr "github.com/piglig/go-qr"

	"github.com/utkayd/qurator/internal/domain"
)

// ECLevel is the QR error-correction level; the domain type is reused so the styling
// stored with a dynamic code and the styling rendered here are the same value.
type ECLevel = domain.ECLevel

// capacity is the maximum byte-mode payload at version 40 per level
// (ISO/IEC 18004 Table 7).
var capacity = map[ECLevel]int{
	domain.ECLow:      2953,
	domain.ECMedium:   2331,
	domain.ECQuartile: 1663,
	domain.ECHigh:     1273,
}

// Capacity returns the maximum encodable payload in bytes for ec, or 0 for an unknown
// level.
func Capacity(ec ECLevel) int { return capacity[ec] }

// levelOrder lists levels from least to most error correction.
var levelOrder = []ECLevel{domain.ECLow, domain.ECMedium, domain.ECQuartile, domain.ECHigh}

// levelRank returns the position of ec in levelOrder, or -1.
func levelRank(ec ECLevel) int {
	for i, l := range levelOrder {
		if l == ec {
			return i
		}
	}
	return -1
}

func toEcc(ec ECLevel) (goqr.Ecc, error) {
	switch ec {
	case domain.ECLow:
		return goqr.Low, nil
	case domain.ECMedium:
		return goqr.Medium, nil
	case domain.ECQuartile:
		return goqr.Quartile, nil
	case domain.ECHigh:
		return goqr.High, nil
	}
	return 0, &InvalidOptionError{Field: "ec_level", Reason: fmt.Sprintf("unknown level %q", string(ec))}
}

// Symbol is an encoded QR module grid. It is immutable once built.
type Symbol struct {
	size    int
	version int
	level   ECLevel
	dark    []bool // row-major, size*size
}

// Size is the side length in modules.
func (s *Symbol) Size() int { return s.size }

// Version is the QR version (1–40).
func (s *Symbol) Version() int { return s.version }

// ECLevel is the level actually encoded in the format information.
func (s *Symbol) ECLevel() ECLevel { return s.level }

// Module reports whether the module at (x, y) is dark. Out-of-range coordinates are
// light, which simplifies neighbour lookups at the edges.
func (s *Symbol) Module(x, y int) bool {
	if x < 0 || y < 0 || x >= s.size || y >= s.size {
		return false
	}
	return s.dark[y*s.size+x]
}

// Encode encodes content in byte mode at exactly ec.
//
// Byte mode is used for every payload — even digits — so that the independent decoder
// returns the payload as byte segments and the round-trip is verified byte-for-byte.
// boostEcl is false: piglig's convenience encoders would otherwise silently promote the
// level (research.md §1). Automatic raising for a logo is done explicitly in the policy
// layer, never here.
func Encode(content []byte, ec ECLevel) (*Symbol, error) {
	ecc, err := toEcc(ec)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, &InvalidOptionError{Field: "content", Reason: "must not be empty"}
	}
	if limit := Capacity(ec); len(content) > limit {
		return nil, &ContentTooLargeError{Limit: limit, Actual: len(content), Level: ec}
	}
	seg, err := goqr.MakeBytes(content)
	if err != nil {
		return nil, fmt.Errorf("qr: make byte segment: %w", err)
	}
	code, err := goqr.EncodeSegments([]*goqr.QrSegment{seg}, ecc, goqr.MinVersion, goqr.MaxVersion, -1, false)
	if err != nil {
		if errors.Is(err, goqr.ErrDataTooLong) {
			return nil, &ContentTooLargeError{Limit: Capacity(ec), Actual: len(content), Level: ec}
		}
		return nil, fmt.Errorf("qr: encode: %w", err)
	}
	n := code.Size()
	s := &Symbol{size: n, version: (n - 17) / 4, level: ec, dark: make([]bool, n*n)}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			s.dark[y*n+x] = code.Module(x, y)
		}
	}
	return s, nil
}
