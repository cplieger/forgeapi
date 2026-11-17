package callmark

import (
	"sync"
	"sync/atomic"
	"testing"
)

// taken spends ctx's bound until it is refused, and reports how many reads it gave.
func taken(t *testing.T, take func() bool) int {
	t.Helper()
	n := 0
	for take() {
		n++
		if n > maxActionReads {
			t.Fatalf("the bound gave more than %d reads", maxActionReads)
		}
	}
	return n
}

func TestTheReadBoundIsClampedToOneThroughTwenty(t *testing.T) {
	for _, test := range []struct {
		name string
		n    int
		want int
	}{
		{name: "negative", n: -3, want: 1},
		{name: "zero", n: 0, want: 1},
		{name: "one", n: 1, want: 1},
		{name: "two", n: 2, want: 2},
		{name: "twenty", n: 20, want: 20},
		{name: "fifty", n: 50, want: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := WithActionReads(t.Context(), test.n)
			if got := taken(t, func() bool { return TakeActionRead(ctx) }); got != test.want {
				t.Errorf("WithActionReads(ctx, %d) gave %d reads, want %d", test.n, got, test.want)
			}
			if ActionReadsLeft(ctx) {
				t.Errorf("ActionReadsLeft after the bound of %d was spent = true, want false", test.n)
			}
		})
	}
}

func TestANestedBoundCannotRaiseTheOuterOne(t *testing.T) {
	outer := WithActionReads(t.Context(), 2)
	inner := WithActionReads(outer, 15)
	if got := taken(t, func() bool { return TakeActionRead(inner) }); got != 2 {
		t.Errorf("WithActionReads(WithActionReads(ctx, 2), 15) gave %d reads, want 2", got)
	}
	if TakeActionRead(outer) {
		t.Error("the outer context gave a read after the nested one spent the bound, want one bound shared by both")
	}
}

func TestAContextWithNoBoundGivesNoRead(t *testing.T) {
	if ActionReadsLeft(t.Context()) {
		t.Error("ActionReadsLeft on a context with no bound = true, want false")
	}
	if TakeActionRead(t.Context()) {
		t.Error("TakeActionRead on a context with no bound = true, want false")
	}
}

func TestTheReadBound_ConcurrentReadersShareIt(t *testing.T) {
	ctx := WithActionReads(t.Context(), 5)
	var given atomic.Int64
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if TakeActionRead(ctx) {
				given.Add(1)
			}
		})
	}
	wg.Wait()
	if got := given.Load(); got != 5 {
		t.Errorf("40 concurrent readers under a bound of 5 were given %d reads, want 5", got)
	}
}

func TestTheWriteMarkIsOnlyWhereItWasSet(t *testing.T) {
	if IsWrite(t.Context()) {
		t.Error("IsWrite on an unmarked context = true, want false")
	}
	marked := WithWrite(t.Context())
	if !IsWrite(marked) {
		t.Error("IsWrite on a context WithWrite marked = false, want true")
	}
	if IsWrite(WithActionReads(t.Context(), 3)) {
		t.Error("IsWrite on a context carrying only a read bound = true, want false")
	}
}
