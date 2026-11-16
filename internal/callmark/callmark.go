// Package callmark holds the request-context marks the root package sets or reads
// and the transport spends or sets: the write mark on a request that changes forge
// state, and the bound of reads one action may pass the mutation reserve with. The
// two packages cannot import each other, so the keys live here.
package callmark

import (
	"context"
	"sync/atomic"
)

// MaxActionReads bounds one action's reads past the reserve, so a poller holding
// one long-lived marked context cannot spend the reserve its writes need.
const MaxActionReads = 20

type writeKey struct{}

type actionKey struct{}

// WithWrite marks ctx as the context of a request that changes forge state.
func WithWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeKey{}, true)
}

// IsWrite reports whether ctx carries [WithWrite]'s mark.
func IsWrite(ctx context.Context) bool {
	marked, _ := ctx.Value(writeKey{}).(bool)
	return marked
}

// WithActionReads gives ctx a bound of n reads, clamped to 1..[MaxActionReads],
// shared by every request and goroutine under the returned context. A ctx that
// already carries a bound is returned unchanged, so a nested call cannot raise it.
func WithActionReads(ctx context.Context, n int) context.Context {
	if bound(ctx) != nil {
		return ctx
	}
	left := &atomic.Int64{}
	left.Store(int64(min(max(n, 1), MaxActionReads)))
	return context.WithValue(ctx, actionKey{}, left)
}

// ActionReadsLeft reports whether ctx carries a bound with a read left in it.
func ActionReadsLeft(ctx context.Context) bool {
	left := bound(ctx)
	return left != nil && left.Load() > 0
}

// TakeActionRead spends one read of ctx's bound, and reports false where ctx
// carries none or it is spent.
func TakeActionRead(ctx context.Context) bool {
	left := bound(ctx)
	if left == nil {
		return false
	}
	for {
		n := left.Load()
		if n <= 0 {
			return false
		}
		if left.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

func bound(ctx context.Context) *atomic.Int64 {
	left, _ := ctx.Value(actionKey{}).(*atomic.Int64)
	return left
}
