package registry

import (
	"errors"
	"sync"
	"testing"
)

func TestAllocIdempotent(t *testing.T) {
	r := New()
	c1, created1, err := r.Alloc("A")
	if err != nil {
		t.Fatalf("alloc A: %v", err)
	}
	if c1 != "0" || !created1 {
		t.Fatalf("alloc A = %q created=%v, want \"0\" created=true", c1, created1)
	}
	c2, created2, err := r.Alloc("A")
	if err != nil {
		t.Fatalf("alloc A again: %v", err)
	}
	if c2 != "0" || created2 {
		t.Fatalf("alloc A again = %q created=%v, want \"0\" created=false", c2, created2)
	}
	st := r.Stats()
	if st.NextCounter != 1 {
		t.Errorf("counter = %d, want 1", st.NextCounter)
	}
	if st.Sources != 1 || st.Codes != 1 {
		t.Errorf("stats = %+v, want sources=codes=1", st)
	}
}

func TestAllocSequential(t *testing.T) {
	r := New()
	for i, want := range []string{"0", "1", "2", "3"} {
		src := string(rune('A' + i))
		c, _, err := r.Alloc(src)
		if err != nil {
			t.Fatalf("alloc %s: %v", src, err)
		}
		if c != want {
			t.Errorf("alloc %s = %q, want %q", src, c, want)
		}
	}
}

func TestResolveFoundAndMissing(t *testing.T) {
	r := New()
	r.Alloc("A")
	r.Alloc("B")
	if src, err := r.Resolve("0"); err != nil || src != "A" {
		t.Errorf("Resolve(0) = %q err=%v, want A", src, err)
	}
	if src, err := r.Resolve("1"); err != nil || src != "B" {
		t.Errorf("Resolve(1) = %q err=%v, want B", src, err)
	}
	// A well-formed but unbound code yields NotFound.
	if _, err := r.Resolve("zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zz unbound): err=%v want ErrNotFound", err)
	}
	// A malformed code yields Format, distinct from NotFound.
	if _, err := r.Resolve("00"); !errors.Is(err, ErrFormat) {
		t.Errorf("Resolve(00): err=%v want ErrFormat", err)
	}
}

func TestReserveNew(t *testing.T) {
	r := New()
	created, err := r.Reserve("X", "abc")
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !created {
		t.Error("created=false, want true for a new binding")
	}
	if src, err := r.Resolve("abc"); err != nil || src != "X" {
		t.Errorf("Resolve(abc) = %q err=%v, want X", src, err)
	}
}

func TestReserveCollision(t *testing.T) {
	r := New()
	if _, err := r.Reserve("X", "abc"); err != nil {
		t.Fatalf("reserve X: %v", err)
	}
	_, err := r.Reserve("Y", "abc")
	var ce *CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("reserve Y on taken code: err=%v want *CollisionError", err)
	}
	if ce.ConflictSource != "X" || ce.Code != "abc" {
		t.Errorf("collision = %+v, want code=abc conflict=X", ce)
	}
	if st := r.Stats(); st.Collisions != 1 {
		t.Errorf("collisions = %d, want 1", st.Collisions)
	}
	// The original binding must be intact.
	if src, err := r.Resolve("abc"); err != nil || src != "X" {
		t.Errorf("after collision Resolve(abc) = %q err=%v, want X", src, err)
	}
}

func TestReserveIdempotent(t *testing.T) {
	r := New()
	if _, err := r.Reserve("X", "abc"); err != nil {
		t.Fatal(err)
	}
	created, err := r.Reserve("X", "abc")
	if err != nil {
		t.Fatalf("reserve X again: %v", err)
	}
	if created {
		t.Error("created=true, want false for idempotent reserve")
	}
	if st := r.Stats(); st.Collisions != 0 {
		t.Errorf("collisions = %d, want 0", st.Collisions)
	}
}

func TestReserveFormatError(t *testing.T) {
	r := New()
	for _, bad := range []string{"", "00", "07", "ab!", "1 2"} {
		if _, err := r.Reserve("X", bad); !errors.Is(err, ErrFormat) {
			t.Errorf("Reserve(code=%q): err=%v want ErrFormat", bad, err)
		}
	}
}

func TestReserveRebindReleasesOldCode(t *testing.T) {
	r := New()
	// Source starts bound to "abc", then moves to a fresh code; the old code
	// must be released so the bijection holds.
	if _, err := r.Reserve("X", "abc"); err != nil {
		t.Fatal(err)
	}
	if created, err := r.Reserve("X", "def"); err != nil || !created {
		t.Fatalf("reserve X to def: created=%v err=%v", created, err)
	}
	if src, err := r.Resolve("def"); err != nil || src != "X" {
		t.Errorf("Resolve(def) = %q err=%v, want X", src, err)
	}
	// Old code "abc" is now free and can be claimed by another source.
	if _, err := r.Resolve("abc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(abc after rebind): err=%v want ErrNotFound", err)
	}
	if st := r.Stats(); st.Sources != 1 || st.Codes != 1 {
		t.Errorf("stats = %+v, want sources=codes=1 after rebind", st)
	}
}

func TestAllocCollisionGuard(t *testing.T) {
	r := New()
	// Reserve the code the counter would otherwise mint first: Encode(0)="0".
	if _, err := r.Reserve("A", "0"); err != nil {
		t.Fatalf("reserve A to 0: %v", err)
	}
	// Allocating a different source must collide and NOT advance the counter.
	_, _, err := r.Alloc("B")
	var ce *CollisionError
	if !errors.As(err, &ce) {
		t.Fatalf("alloc B: err=%v want *CollisionError", err)
	}
	if ce.ConflictSource != "A" || ce.Code != "0" {
		t.Errorf("collision = %+v, want code=0 conflict=A", ce)
	}
	st := r.Stats()
	if st.NextCounter != 0 {
		t.Errorf("counter advanced to %d, want 0", st.NextCounter)
	}
	if st.Collisions != 1 {
		t.Errorf("collisions = %d, want 1", st.Collisions)
	}
	// The already-bound source is still served idempotently.
	if c, created, err := r.Alloc("A"); err != nil || c != "0" || created {
		t.Errorf("alloc A after collision = %q created=%v err=%v, want 0/false", c, created, err)
	}
}

func TestAllocBatchDedup(t *testing.T) {
	r := New()
	res, err := r.AllocBatch([]string{"A", "B", "A"})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	want := []AllocResult{
		{Source: "A", Code: "0", Created: true},
		{Source: "B", Code: "1", Created: true},
		{Source: "A", Code: "0", Created: false},
	}
	for i, w := range want {
		if res[i] != w {
			t.Errorf("result[%d] = %+v, want %+v", i, res[i], w)
		}
	}
	st := r.Stats()
	if st.Sources != 2 || st.Codes != 2 || st.NextCounter != 2 {
		t.Errorf("stats = %+v, want sources=codes=2 next=2", st)
	}
}

func TestAllocBatchEmpty(t *testing.T) {
	r := New()
	res, err := r.AllocBatch(nil)
	if err != nil {
		t.Fatalf("batch empty: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("empty batch returned %d results, want 0", len(res))
	}
}

func TestAllocBatchInvalidSource(t *testing.T) {
	r := New()
	if _, err := r.AllocBatch([]string{"A", "", "B"}); !errors.Is(err, ErrEmptySource) {
		t.Errorf("batch with empty source: err=%v want ErrEmptySource", err)
	}
	// No state mutated for the empty-source entry.
	if st := r.Stats(); st.Sources != 0 {
		t.Errorf("stats after failed batch = %+v, want empty", st)
	}
}

func TestStatsBijectionHolds(t *testing.T) {
	r := New()
	r.Alloc("A")
	r.Alloc("B")
	r.Reserve("C", "zz")
	r.Reserve("D", "ab")
	st := r.Stats()
	if st.Sources != st.Codes {
		t.Errorf("bijection broken: sources=%d codes=%d", st.Sources, st.Codes)
	}
}

func TestConcurrentAlloc(t *testing.T) {
	r := New()
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			r.Alloc(string(rune('A' + i%26)) + "-" + itoa(i))
		}(i)
	}
	wg.Wait()
	st := r.Stats()
	// Every concurrent Alloc used a distinct source, so each minted a unique
	// code: the bijection must hold and the counter must match the source
	// count.
	if st.Sources != st.Codes {
		t.Errorf("bijection broken: sources=%d codes=%d", st.Sources, st.Codes)
	}
	if st.NextCounter != uint64(goroutines) {
		t.Errorf("counter = %d, want %d", st.NextCounter, goroutines)
	}
}

// itoa formats a non-negative int as a decimal string without pulling in
// strconv (keeps the test import list focused on the package under test).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}
