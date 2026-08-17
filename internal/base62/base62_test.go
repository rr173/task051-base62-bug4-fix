package base62

import (
	"errors"
	"testing"
)

func TestEncodeKnown(t *testing.T) {
	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "A"},
		{35, "Z"},
		{36, "a"},
		{61, "z"},
		{62, "10"},
		{63, "11"},
		{72, "1A"},  // 72 = 1*62 + 10 -> "1A"
		{124, "20"}, // 124 = 2*62 + 0 -> "20"
	}
	for _, c := range cases {
		if got := Encode(c.n); got != c.want {
			t.Errorf("Encode(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestEncodeNoLeadingZero(t *testing.T) {
	// No encoding of a positive value may start with "0".
	for n := uint64(1); n < 5000; n++ {
		got := Encode(n)
		if got[0] == '0' {
			t.Fatalf("Encode(%d) = %q has leading zero", n, got)
		}
	}
}

func TestDecodeValid(t *testing.T) {
	cases := []struct {
		s    string
		want uint64
	}{
		{"0", 0},
		{"1", 1},
		{"9", 9},
		{"A", 10},
		{"Z", 35},
		{"a", 36},
		{"z", 61},
		{"10", 62},
		{"11", 63},
		{"1A", 72},
		{"20", 124},
		{"zz", 3843}, // 61*62 + 61
	}
	for _, c := range cases {
		got, err := Decode(c.s)
		if err != nil {
			t.Fatalf("Decode(%q): unexpected err %v", c.s, err)
		}
		if got != c.want {
			t.Errorf("Decode(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestDecodeFormatErrors(t *testing.T) {
	bad := []string{
		"",     // empty
		"abc!", // illegal char
		"1-2",  // illegal char
		" 1",   // leading space
		"1 ",   // trailing space
		"00",   // leading zero, multi-char
		"07",   // leading zero, multi-char
		"0A",   // leading zero, multi-char
		"ab+",  // illegal char
	}
	for _, s := range bad {
		if _, err := Decode(s); !errors.Is(err, ErrFormat) {
			t.Errorf("Decode(%q): err=%v, want ErrFormat", s, err)
		}
	}
}

func TestDecodeOverflow(t *testing.T) {
	// "zzzzzzzzzzz" (11 chars) is the largest 11-char value and exceeds
	// uint64.
	if _, err := Decode("zzzzzzzzzzz"); !errors.Is(err, ErrOverflow) {
		t.Errorf("Decode(zzzzzzzzzzz): err=%v, want ErrOverflow", err)
	}
	// A 12-char canonical string necessarily overflows (min value 62^11 >
	// 2^64-1).
	if _, err := Decode("100000000000"); !errors.Is(err, ErrOverflow) {
		t.Errorf("Decode(100000000000): err=%v, want ErrOverflow", err)
	}
	// An 11-char string that does NOT overflow: 62^10 == 839299365868340224.
	got, err := Decode("10000000000")
	if err != nil {
		t.Fatalf("Decode(10000000000): unexpected err %v", err)
	}
	if want := uint64(839299365868340224); got != want {
		t.Errorf("Decode(10000000000) = %d, want %d", got, want)
	}
}

func TestDecodeCaseSensitive(t *testing.T) {
	// 'A' (value 10) and 'a' (value 36) are distinct digits.
	a, err := Decode("A")
	if err != nil || a != 10 {
		t.Fatalf("Decode(A) = %d, err=%v, want 10", a, err)
	}
	low, err := Decode("a")
	if err != nil || low != 36 {
		t.Fatalf("Decode(a) = %d, err=%v, want 36", low, err)
	}
}

func TestRoundTrip(t *testing.T) {
	values := []uint64{
		0, 1, 61, 62, 63, 100, 9999, 3843,
		839299365868340224,              // 62^10, boundary
		839299365868340223,              // 62^10 - 1, max 10-char value
		18446744073709551615,            // uint64 max (11-char encoding)
		18446744073709551614,            // uint64 max - 1
	}
	for _, v := range values {
		enc := Encode(v)
		got, err := Decode(enc)
		if err != nil {
			t.Fatalf("Decode(Encode(%d)=%q): %v", v, enc, err)
		}
		if got != v {
			t.Errorf("round-trip %d -> %q -> %d", v, enc, got)
		}
	}
}

func TestEncodeUint64Max(t *testing.T) {
	maxU := uint64(18446744073709551615)
	enc := Encode(maxU)
	if len(enc) != 11 {
		t.Fatalf("Encode(uint64max) = %q len %d, want 11 chars", enc, len(enc))
	}
	got, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode(uint64max enc): %v", err)
	}
	if got != maxU {
		t.Errorf("uint64max round-trip: got %d", got)
	}
}

func TestIsValid(t *testing.T) {
	valid := []string{"0", "1", "10", "A", "z", "zz", "LygHa16AHYF", "10000000000"}
	for _, s := range valid {
		if !IsValid(s) {
			t.Errorf("IsValid(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "00", "07", "ab!", "zzzzzzzzzzz", "100000000000"}
	for _, s := range invalid {
		if IsValid(s) {
			t.Errorf("IsValid(%q) = true, want false", s)
		}
	}
}
