// Package base62 implements a Base62 codec used by the short-code registry.
//
// The character set is fixed to "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
// followed by "abcdefghijklmnopqrstuvwxyz" (so 0-9, A-Z, a-z map to digit
// values 0..9, 10..35, 36..61). Encoding is big-endian (most significant
// digit first) and produces the canonical form: no leading zeros except for
// the value 0, which encodes to the single character "0".
//
// Decoding distinguishes a syntactically invalid string (ErrFormat) from a
// syntactically valid string whose value exceeds the uint64 range
// (ErrOverflow).
package base62

import (
	"errors"
	"math/bits"
)

// Charset is the fixed symbol table, indexed by digit value.
const Charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// ErrFormat is returned when an input string is not a syntactically valid
// Base62 string: empty, containing a character outside Charset, or a
// multi-character string with a leading "0".
var ErrFormat = errors.New("base62: invalid format")

// ErrOverflow is returned when a valid Base62 string denotes a value that
// does not fit in a uint64.
var ErrOverflow = errors.New("base62: overflow")

// charValue maps a byte to its digit value. ok is false if the byte is not in
// Charset. The switch is ordered by expected frequency (digits, then upper,
// then lower case) and keeps allocation off the hot path.
func charValue(c byte) (v uint64, ok bool) {
	switch {
	case c >= '0' && c <= '9':
		return uint64(c - '0'), true
	case c >= 'A' && c <= 'Z':
		return uint64(c-'A') + 10, true
	case c >= 'a' && c <= 'z':
		return uint64(c-'a') + 36, true
	default:
		return 0, false
	}
}

// Encode returns the canonical Base62 encoding of n. n==0 encodes to "0".
func Encode(n uint64) string {
	if n == 0 {
		return "0"
	}
	// 62^11 exceeds 2^64-1, so 11 characters always suffice; 16 is a safe
	// stack-backed upper bound that avoids heap allocation for the common
	// case.
	var buf [16]byte
	i := len(buf)
	const base = uint64(62)
	for n > 0 {
		i--
		buf[i] = Charset[n%base]
		n /= base
	}
	return string(buf[i:])
}

// Decode parses s as a canonical Base62 string and returns its value.
//
// It distinguishes three outcomes:
//   - a syntactically invalid string (empty, an illegal character, or a
//     multi-character string with a leading "0") yields ErrFormat;
//   - a syntactically valid string whose value exceeds the uint64 range
//     yields ErrOverflow (a canonical string longer than 11 characters
//     necessarily overflows, since its leading digit is non-zero and
//     62^11 > 2^64-1);
//   - otherwise the decoded value is returned with a nil error.
func Decode(s string) (uint64, error) {
	if len(s) == 0 {
		return 0, ErrFormat
	}
	// Validate the leading digit and reject leading zeros on multi-char
	// strings (a leading "0" is only legal for the single-character "0").
	if _, ok := charValue(s[0]); !ok {
		return 0, ErrFormat
	}
	if len(s) >= 2 && s[0] == '0' {
		return 0, ErrFormat
	}
	for i := 1; i < len(s); i++ {
		if _, ok := charValue(s[i]); !ok {
			return 0, ErrFormat
		}
	}
	// A canonical string longer than 11 characters cannot fit in a uint64.
	if len(s) > 11 {
		return 0, ErrOverflow
	}
	const base = uint64(62)
	var r uint64
	for i := 0; i < len(s); i++ {
		d, _ := charValue(s[i])
		// r = r*base + d, with overflow detected via math/bits so the
		// arithmetic itself cannot silently wrap.
		hi, lo := bits.Mul64(r, base)
		if hi != 0 {
			return 0, ErrOverflow
		}
		sum, carry := bits.Add64(lo, d, 0)
		if carry != 0 {
			return 0, ErrOverflow
		}
		r = sum
	}
	return r, nil
}

// IsValid reports whether s is a decodable Base62 code: a syntactically
// valid, canonical string whose value fits in a uint64. It accepts exactly
// the strings Encode can produce.
func IsValid(s string) bool {
	_, err := Decode(s)
	return err == nil
}
