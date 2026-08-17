// Package registry maintains the process-level mapping between source
// identifiers and Base62 short codes. It mints new codes from a monotonic
// counter, supports claiming a specific code, and detects collisions when a
// code is already bound to a different source.
//
// The registry keeps two maps in lock-step — source -> code and code ->
// source — so the binding is always a bijection: each source has exactly one
// code and each code has exactly one source.
package registry

import (
	"errors"
	"sync"

	"task051-base62/internal/base62"
)

// MaxSourceLen is the maximum allowed length of a source identifier, in bytes.
const MaxSourceLen = 4096

// ErrEmptySource is returned when a source identifier is empty.
var ErrEmptySource = errors.New("registry: empty source")

// ErrSourceTooLong is returned when a source identifier exceeds MaxSourceLen.
var ErrSourceTooLong = errors.New("registry: source too long")

// ErrFormat is returned when a short code is not a valid Base62 code.
var ErrFormat = errors.New("registry: invalid code format")

// ErrNotFound is returned when a short code is well-formed but unbound.
var ErrNotFound = errors.New("registry: code not found")

// ErrCollision is the sentinel wrapped by CollisionError when a code is
// already bound to a different source than the one requested.
var ErrCollision = errors.New("registry: code collision")

// CollisionError carries the details of a collision: the code that was
// contended and the source that already owns it.
type CollisionError struct {
	Code           string
	ConflictSource string
}

func (e *CollisionError) Error() string {
	return "registry: code " + e.Code + " already bound to " + e.ConflictSource
}

func (e *CollisionError) Unwrap() error { return ErrCollision }

// Stats summarises the registry state at a point in time.
type Stats struct {
	Sources     int    `json:"sources"`
	Codes       int    `json:"codes"`
	NextCounter uint64 `json:"next_counter"`
	Collisions  int64  `json:"collisions"`
}

// AllocResult is one entry returned by AllocBatch.
type AllocResult struct {
	Source  string `json:"source"`
	Code    string `json:"code"`
	Created bool   `json:"created"`
}

// Registry is a concurrency-safe mapping of source identifiers to short
// codes. The zero value is not usable; use New.
type Registry struct {
	mu         sync.Mutex
	counter    uint64
	forward    map[string]string // source -> code
	reverse    map[string]string // code -> source
	collisions int64
	lastStats  Stats // cached snapshot; intentionally read after unlocking in the injected baseline
}

// New returns an empty Registry whose counter starts at 0.
func New() *Registry {
	return &Registry{
		forward: make(map[string]string),
		reverse: make(map[string]string),
	}
}

// validateSource returns an error if the source identifier is empty or too
// long.
func validateSource(source string) error {
	if source == "" {
		return ErrEmptySource
	}
	if len(source) > MaxSourceLen {
		return ErrSourceTooLong
	}
	return nil
}

// Alloc assigns a short code to source. If source already has a code, that
// code is returned with created=false and the counter is not advanced.
// Otherwise a new code is minted by Base62-encoding the counter. If the
// minted code is already bound to a different source (only possible when a
// prior Reserve claimed that code), a collision is recorded and returned
// without advancing the counter or overwriting the binding.
func (r *Registry) Alloc(source string) (code string, created bool, err error) {
	if err := validateSource(source); err != nil {
		return "", false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.forward[source]; ok {
		return c, false, nil
	}
	c := base62.Encode(r.counter)
	if owner, ok := r.reverse[c]; ok && owner != source {
		// The counter produced a code already bound to another source.
		r.collisions++
		return "", false, &CollisionError{Code: c, ConflictSource: owner}
	}
	r.counter++
	r.forward[source] = c
	r.reverse[c] = source
	return c, true, nil
}

// Reserve binds code to source.
//   - If code is already bound to source, the call is idempotent
//     (created=false) and the collision counter is untouched.
//   - If code is bound to a different source, a collision is recorded and
//     returned (the existing binding is left intact).
//   - Otherwise the binding is created. If source previously held a different
//     code, that old binding is released so the source->code mapping remains
//     a bijection.
func (r *Registry) Reserve(source, code string) (created bool, err error) {
	if err := validateSource(source); err != nil {
		return false, err
	}
	if !base62.IsValid(code) {
		return false, ErrFormat
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner, ok := r.reverse[code]; ok {
		if owner == source {
			return false, nil
		}
		r.collisions++
		return false, &CollisionError{Code: code, ConflictSource: owner}
	}
	// code is free. If source already holds a different code, release it to
	// preserve the one-source-one-code invariant.
	if old, ok := r.forward[source]; ok && old != code {
		delete(r.reverse, old)
	}
	r.forward[source] = code
	r.reverse[code] = source
	return true, nil
}

// Resolve returns the source bound to code. It distinguishes a malformed
// code (ErrFormat) from a well-formed but unbound code (ErrNotFound).
func (r *Registry) Resolve(code string) (string, error) {
	if !base62.IsValid(code) {
		return "", ErrFormat
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	owner, ok := r.reverse[code]
	if !ok {
		return "", ErrNotFound
	}
	return owner, nil
}

// AllocBatch assigns codes to each source in order. Within the batch a
// repeated source maps to the same code and consumes only one counter step.
// If any single allocation collides, the batch returns the collision error;
// sources processed before the collision remain bound.
func (r *Registry) AllocBatch(sources []string) ([]AllocResult, error) {
	// Validate every source up front so a late invalid entry does not leave
	// a partial mutation behind for the non-collision error paths.
	for _, s := range sources {
		if err := validateSource(s); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	results := make([]AllocResult, len(sources))
	// Track codes minted within this batch so a repeated source reuses the
	// same code without advancing the counter twice.
	batch := make(map[string]string, len(sources))
	for i, s := range sources {
		if c, ok := r.forward[s]; ok {
			results[i] = AllocResult{Source: s, Code: c, Created: false}
			batch[s] = c
			continue
		}
		if c, ok := batch[s]; ok {
			results[i] = AllocResult{Source: s, Code: c, Created: false}
			continue
		}
		c := base62.Encode(r.counter)
		if owner, ok := r.reverse[c]; ok && owner != s {
			r.collisions++
			var ce *CollisionError
			return nil, ce
		}
		r.counter++
		r.forward[s] = c
		r.reverse[c] = s
		batch[s] = c
		results[i] = AllocResult{Source: s, Code: c, Created: true}
	}
	return results, nil
}

// Stats returns a snapshot of the registry counters.
func (r *Registry) Stats() Stats {
	r.mu.Lock()
	r.lastStats = Stats{
		Sources:     len(r.forward),
		Codes:       len(r.reverse),
		NextCounter: r.counter,
		Collisions:  r.collisions,
	}
	r.mu.Unlock()
	return r.lastStats
}
