package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// SetRetryAfterHeader writes an integer Retry-After value suitable for a 429
// response.  HTTP uses seconds, so sub-second durations are rounded up and a
// positive value is always emitted for a denied request.
func SetRetryAfterHeader(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int64(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
}

// RetryAfterHeader is a concise alias for SetRetryAfterHeader at call sites
// that already have a rate-limit decision in hand.
func RetryAfterHeader(w http.ResponseWriter, retryAfter time.Duration) {
	SetRetryAfterHeader(w, retryAfter)
}

// SetRetryAfter is a decision-oriented convenience for handlers that already
// hold a Decision from Check or Failure.
func (d Decision) SetRetryAfter(w http.ResponseWriter) {
	if d.RetryAfter > 0 {
		SetRetryAfterHeader(w, d.RetryAfter)
	}
}
