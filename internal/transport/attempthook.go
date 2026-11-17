package transport

import (
	"context"
	"net/http"
	"sync/atomic"
)

// hookAttempts installs the consumer's attempt hook over next, telling a
// refusal, an error the hook answered without passing the attempt on, from a
// failure beneath it: only a refusal comes back as a [*refusedAttemptError].
func hookAttempts(next http.RoundTripper, wrap func(http.RoundTripper) http.RoundTripper) http.RoundTripper {
	return attemptHook{hook: wrap(passedOn{next: next})}
}

// passedKey carries one attempt's flag, which [passedOn] sets once the hook has
// passed that attempt on. The hook derives the request it passes from the one
// it got, so the flag reaches beneath it whatever the hook changes.
type passedKey struct{}

type attemptHook struct{ hook http.RoundTripper }

func (h attemptHook) RoundTrip(r *http.Request) (*http.Response, error) {
	passed := new(atomic.Bool)
	resp, err := h.hook.RoundTrip(r.WithContext(context.WithValue(r.Context(), passedKey{}, passed)))
	if err != nil && !passed.Load() {
		return nil, &refusedAttemptError{err: err}
	}
	return resp, err
}

type passedOn struct{ next http.RoundTripper }

func (p passedOn) RoundTrip(r *http.Request) (*http.Response, error) {
	if passed, ok := r.Context().Value(passedKey{}).(*atomic.Bool); ok {
		passed.Store(true)
	}
	return p.next.RoundTrip(r)
}

// refusedAttemptError is the attempt hook's own error for an attempt it sent nowhere,
// which [retryCheck] never retries and [Conn.transportError] answers as
// [forgeapi.CodeAttemptRefused].
type refusedAttemptError struct{ err error }

func (e *refusedAttemptError) Error() string { return e.err.Error() }

func (e *refusedAttemptError) Unwrap() error { return e.err }
