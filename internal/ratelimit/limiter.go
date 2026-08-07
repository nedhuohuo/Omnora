// Package ratelimit provides small, in-process limiters for credential
// verification endpoints.  The limiter intentionally stores only opaque
// caller-supplied keys.  Callers should derive those keys with a keyed hash
// before handing them to this package.
package ratelimit

import (
	"hash/fnv"
	"sync"
	"time"
)

// Scope identifies an independently rate-limited credential flow.
type Scope string

const (
	ScopeLogin          Scope = "login"
	ScopeReauthenticate Scope = "reauthenticate"
	ScopeTOTP           Scope = "totp"
	ScopeShareExchange  Scope = "share_exchange"
	ScopeInitialize     Scope = "initialize"
)

// Keys contains the opaque IP and subject keys for one attempt.  The values
// are deliberately not normalized or logged by this package.
type Keys struct {
	IP      string
	Subject string
}

// Decision is the result of checking both dimensions.  RetryAfter is the
// longest active cooldown (zero when the request is allowed).
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

// Options controls the bounded limiter.  Clock and Now are aliases so callers
// can use either name when injecting a deterministic clock in tests.  A zero
// capacity selects the conservative default.
type Options struct {
	Clock       func() time.Time
	Now         func() time.Time
	Shards      int
	ShardCount  int
	Capacity    int
	MaxKeys     int
	MaxEntries  int
	MaxCapacity int
}

const (
	defaultShards   = 64
	defaultCapacity = 4096
)

// Limiter is safe for concurrent use.  Normal keys are bounded by capacity;
// each scope/dimension also has one fixed overflow bucket.  Overflow buckets
// keep an attacker from exhausting the map with unbounded new subjects while
// retaining useful throttling once the normal capacity is full.
type Limiter struct {
	clock    func() time.Time
	shards   []limiterShard
	capacity int

	admissionMu sync.Mutex
	normalKeys  int

	overflowMu sync.Mutex
	overflow   map[bucketID]*state
}

type limiterShard struct {
	mu    sync.Mutex
	items map[string]*state
}

type state struct {
	failures    int
	cooldownEnd time.Time
}

type bucketID struct {
	scope Scope
	dim   dimension
}

type dimension uint8

const (
	dimensionIP dimension = iota
	dimensionSubject
)

// New constructs a limiter.  It never starts background goroutines; expired
// state is compacted opportunistically during requests.
func New(opts Options) *Limiter {
	shards := opts.Shards
	if shards <= 0 {
		shards = opts.ShardCount
	}
	if shards <= 0 {
		shards = defaultShards
	}
	capacity := opts.Capacity
	if capacity <= 0 {
		capacity = opts.MaxKeys
	}
	if capacity <= 0 {
		capacity = opts.MaxEntries
	}
	if capacity <= 0 {
		capacity = opts.MaxCapacity
	}
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	clock := opts.Clock
	if clock == nil {
		clock = opts.Now
	}
	if clock == nil {
		clock = time.Now
	}
	l := &Limiter{
		clock:    clock,
		shards:   make([]limiterShard, shards),
		capacity: capacity,
		overflow: make(map[bucketID]*state),
	}
	for i := range l.shards {
		l.shards[i].items = make(map[string]*state)
	}
	return l
}

// Check reports whether either dimension is currently cooling down.  A
// previously unseen key is allowed and does not consume bounded capacity.
func (l *Limiter) Check(scope Scope, keys Keys) Decision {
	scope = normalizeScope(scope)
	now := l.now()
	ip := l.lookup(scope, dimensionIP, keys.IP)
	subject := l.lookup(scope, dimensionSubject, keys.Subject)
	return combineDecision(now, ip, subject)
}

// Failure records one failed credential attempt in both dimensions and
// returns the resulting decision.  Already blocked dimensions are not
// incremented again; this prevents a request flood during a cooldown from
// escalating the bucket indefinitely.
func (l *Limiter) Failure(scope Scope, keys Keys) Decision {
	scope = normalizeScope(scope)
	now := l.now()
	ip := l.failure(scope, dimensionIP, keys.IP, now)
	subject := l.failure(scope, dimensionSubject, keys.Subject, now)
	return combineDecision(now, ip, subject)
}

// Success clears only the subject bucket.  The IP bucket deliberately remains
// intact so a successful account does not erase pressure from one client
// attacking other accounts.
func (l *Limiter) Success(scope Scope, subject string) {
	scope = normalizeScope(scope)
	if subject == "" {
		return
	}
	key := makeKey(scope, dimensionSubject, subject)
	shard := l.shardFor(key)
	shard.mu.Lock()
	if value, ok := shard.items[key]; ok {
		delete(shard.items, key)
		shard.mu.Unlock()
		l.admissionMu.Lock()
		if l.normalKeys > 0 {
			l.normalKeys--
		}
		l.admissionMu.Unlock()
		_ = value
		return
	}
	shard.mu.Unlock()

	// A shared overflow bucket intentionally has no per-subject identity.  Do
	// not clear it on one successful subject: doing so would let a successful
	// login for account B erase pressure caused by an attacker targeting
	// account A.  Overflow remains fail-closed until its cooldown expires.
}

func (l *Limiter) failure(scope Scope, dim dimension, value string, now time.Time) *state {
	if value == "" {
		return nil
	}
	key := makeKey(scope, dim, value)
	shard := l.shardFor(key)
	shard.mu.Lock()
	item := shard.items[key]
	if item != nil {
		if now.Before(item.cooldownEnd) {
			result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
			shard.mu.Unlock()
			return result
		}
		item.failures++
		item.cooldownEnd = now.Add(cooldownFor(dim, item.failures))
		result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
		shard.mu.Unlock()
		return result
	}
	shard.mu.Unlock()
	// If capacity was exhausted previously, this dimension is represented by
	// one fixed overflow state.  Its presence is checked before attempting a
	// new normal-key admission.
	l.overflowMu.Lock()
	overflow := l.overflow[bucketID{scope: scope, dim: dim}]
	if overflow != nil {
		if now.Before(overflow.cooldownEnd) {
			result := &state{failures: overflow.failures, cooldownEnd: overflow.cooldownEnd}
			l.overflowMu.Unlock()
			return result
		}
		overflow.failures++
		overflow.cooldownEnd = now.Add(cooldownFor(dim, overflow.failures))
		result := &state{failures: overflow.failures, cooldownEnd: overflow.cooldownEnd}
		l.overflowMu.Unlock()
		return result
	}
	l.overflowMu.Unlock()

	// Admit a normal key only when capacity remains.  Admission is serialized
	// separately from shard mutation so normal requests do not contend on a
	// single global lock.
	l.admissionMu.Lock()
	if l.normalKeys < l.capacity {
		l.normalKeys++
		l.admissionMu.Unlock()
		shard = l.shardFor(key)
		shard.mu.Lock()
		item := shard.items[key]
		if item == nil {
			item = &state{}
			shard.items[key] = item
		} else {
			// Another goroutine admitted this key between our capacity check
			// and shard lock.  Do not charge the same key twice against the
			// normal-key budget.
			l.admissionMu.Lock()
			if l.normalKeys > 0 {
				l.normalKeys--
			}
			l.admissionMu.Unlock()
		}
		item.failures++
		item.cooldownEnd = now.Add(cooldownFor(dim, item.failures))
		result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
		shard.mu.Unlock()
		return result
	}
	l.admissionMu.Unlock()
	return l.overflowFailure(scope, dim, now)
}

func (l *Limiter) overflowFailure(scope Scope, dim dimension, now time.Time) *state {
	id := bucketID{scope: scope, dim: dim}
	l.overflowMu.Lock()
	item := l.overflow[id]
	if item == nil {
		item = &state{}
		l.overflow[id] = item
	}
	if now.Before(item.cooldownEnd) {
		result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
		l.overflowMu.Unlock()
		return result
	}
	item.failures++
	item.cooldownEnd = now.Add(cooldownFor(dim, item.failures))
	result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
	l.overflowMu.Unlock()
	return result
}

func (l *Limiter) lookup(scope Scope, dim dimension, value string) *state {
	if value == "" {
		return nil
	}
	key := makeKey(scope, dim, value)
	shard := l.shardFor(key)
	shard.mu.Lock()
	item := shard.items[key]
	if item != nil {
		now := l.now()
		if !now.Before(item.cooldownEnd) && item.failures == 0 {
			delete(shard.items, key)
			shard.mu.Unlock()
			l.admissionMu.Lock()
			if l.normalKeys > 0 {
				l.normalKeys--
			}
			l.admissionMu.Unlock()
			return nil
		}
		result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
		shard.mu.Unlock()
		return result
	}
	shard.mu.Unlock()

	l.overflowMu.Lock()
	item = l.overflow[bucketID{scope: scope, dim: dim}]
	if item == nil {
		l.overflowMu.Unlock()
		return nil
	}
	if !l.now().Before(item.cooldownEnd) && item.failures == 0 {
		delete(l.overflow, bucketID{scope: scope, dim: dim})
		l.overflowMu.Unlock()
		return nil
	}
	result := &state{failures: item.failures, cooldownEnd: item.cooldownEnd}
	l.overflowMu.Unlock()
	return result
}

func (l *Limiter) shardFor(key string) *limiterShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &l.shards[uint(h.Sum32())%uint(len(l.shards))]
}

func (l *Limiter) now() time.Time {
	return l.clock().UTC()
}

func combineDecision(now time.Time, values ...*state) Decision {
	decision := Decision{Allowed: true}
	for _, value := range values {
		if value == nil || !now.Before(value.cooldownEnd) {
			continue
		}
		decision.Allowed = false
		if retry := value.cooldownEnd.Sub(now); retry > decision.RetryAfter {
			decision.RetryAfter = retry
		}
	}
	return decision
}

func cooldownFor(dim dimension, failures int) time.Duration {
	if dim == dimensionIP {
		switch {
		case failures >= 80:
			return time.Hour
		case failures >= 40:
			return 5 * time.Minute
		case failures >= 20:
			return time.Minute
		default:
			return 0
		}
	}
	switch {
	case failures >= 20:
		return 30 * time.Minute
	case failures >= 10:
		return 5 * time.Minute
	case failures >= 5:
		return time.Minute
	default:
		return 0
	}
}

func makeKey(scope Scope, dim dimension, value string) string {
	// Length-prefixing avoids collisions when opaque values contain separators.
	return string(scope) + ":" + string(rune('0'+dim)) + ":" + value
}

func normalizeScope(scope Scope) Scope {
	switch scope {
	case ScopeLogin, ScopeReauthenticate, ScopeTOTP, ScopeShareExchange, ScopeInitialize:
		return scope
	default:
		// Scope is a closed set.  Collapsing accidental/attacker-controlled
		// values keeps the fixed overflow map bounded even when a caller has
		// not validated input at its boundary.
		return "unknown"
	}
}
