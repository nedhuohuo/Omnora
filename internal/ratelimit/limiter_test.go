package ratelimit

import (
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func TestLimiterProgressiveSubjectCooldowns(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	l := New(Options{Clock: clock.Now, Capacity: 32})
	keys := Keys{IP: "ip-a", Subject: "subject-a"}
	for attempt := 1; attempt <= 4; attempt++ {
		if got := l.Failure(ScopeLogin, keys); !got.Allowed || got.RetryAfter != 0 {
			t.Fatalf("failure %d = %#v, want allowed", attempt, got)
		}
	}
	if got := l.Failure(ScopeLogin, keys); got.Allowed || got.RetryAfter != time.Minute {
		t.Fatalf("failure 5 = %#v, want one minute cooldown", got)
	}
	clock.Advance(time.Minute)
	if got := l.Check(ScopeLogin, keys); !got.Allowed {
		t.Fatalf("check after cooldown = %#v, want allowed", got)
	}
	for attempt := 6; attempt <= 9; attempt++ {
		l.Failure(ScopeLogin, keys)
		clock.Advance(time.Minute)
	}
	if got := l.Failure(ScopeLogin, keys); got.Allowed || got.RetryAfter != 5*time.Minute {
		t.Fatalf("failure 10 = %#v, want five minute cooldown", got)
	}
	clock.Advance(5 * time.Minute)
	for attempt := 11; attempt <= 19; attempt++ {
		l.Failure(ScopeLogin, keys)
		clock.Advance(5 * time.Minute)
	}
	if got := l.Failure(ScopeLogin, keys); got.Allowed || got.RetryAfter != 30*time.Minute {
		t.Fatalf("failure 20 = %#v, want thirty minute cooldown", got)
	}
}

func TestLimiterProgressiveIPCooldowns(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	l := New(Options{Clock: clock.Now, Capacity: 256})
	for attempt := 1; attempt <= 19; attempt++ {
		if got := l.Failure(ScopeLogin, Keys{IP: "ip-a", Subject: "subject-" + strconv.Itoa(attempt)}); !got.Allowed {
			t.Fatalf("failure %d = %#v, want allowed", attempt, got)
		}
	}
	if got := l.Failure(ScopeLogin, Keys{IP: "ip-a", Subject: "subject-20"}); got.Allowed || got.RetryAfter != time.Minute {
		t.Fatalf("ip failure 20 = %#v, want one minute cooldown", got)
	}
}

func TestLimiterSuccessOnlyClearsSubject(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	l := New(Options{Clock: clock.Now, Capacity: 32})
	keys := Keys{IP: "ip-a", Subject: "subject-a"}
	for i := 0; i < 5; i++ {
		l.Failure(ScopeLogin, keys)
	}
	for i := 5; i < 20; i++ {
		l.Failure(ScopeLogin, Keys{IP: keys.IP, Subject: "subject-" + strconv.Itoa(i)})
	}
	l.Success(ScopeLogin, keys.Subject)
	if got := l.Check(ScopeLogin, keys); got.Allowed || got.RetryAfter != time.Minute {
		t.Fatalf("check after subject success = %#v, want IP cooldown", got)
	}
	for i := 0; i < 4; i++ {
		if got := l.Failure(ScopeLogin, Keys{IP: "ip-b", Subject: keys.Subject}); !got.Allowed {
			t.Fatalf("subject failure after success %d = %#v, want allowed", i, got)
		}
	}
}

func TestLimiterCapacityUsesOverflowBucket(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	l := New(Options{Clock: clock.Now, Capacity: 1})
	// The first subject is admitted normally.  Subsequent subjects share the
	// bounded subject overflow bucket rather than growing the map.
	for i := 0; i < 4; i++ {
		if got := l.Failure(ScopeLogin, Keys{Subject: "subject-a", IP: "ip-a"}); !got.Allowed {
			t.Fatalf("first subject failure %d = %#v", i, got)
		}
	}
	if got := l.Failure(ScopeLogin, Keys{Subject: "subject-a", IP: "ip-a"}); got.Allowed {
		t.Fatalf("first subject fifth failure = %#v, want denied", got)
	}
	if got := l.Failure(ScopeLogin, Keys{Subject: "subject-b", IP: "ip-b"}); got.Allowed {
		t.Fatalf("overflow subject shares the active bucket = %#v, want denied", got)
	}
}

func TestSetRetryAfterHeaderRoundsUp(t *testing.T) {
	recorder := httptest.NewRecorder()
	SetRetryAfterHeader(recorder, 1500*time.Millisecond)
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
}

func TestLimiterConcurrentAccess(t *testing.T) {
	l := New(Options{Capacity: 64})
	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				keys := Keys{IP: "ip-" + strconv.Itoa(worker%4), Subject: "subject-" + strconv.Itoa(worker%8)}
				l.Check(ScopeLogin, keys)
				l.Failure(ScopeLogin, keys)
				if i%10 == 0 {
					l.Success(ScopeLogin, keys.Subject)
				}
			}
		}(worker)
	}
	wg.Wait()
}
