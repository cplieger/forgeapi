package conformance

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// figureMethod is a read every product serves with one REST request.
const figureMethod = "Identity.Whoami"

// figureClient is what every family's client reports of its rate-limit figures.
type figureClient interface {
	BudgetState() forgeapi.BudgetState
	PoolOf(req *http.Request) string
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// figureRows is p's spec.Figures rows by field.
func figureRows(t *testing.T, p spec.Product) map[string]spec.Figure {
	t.Helper()
	rows := map[string]spec.Figure{}
	for _, f := range spec.Figures {
		if f.Product == p {
			rows[f.Field] = f
		}
	}
	for _, field := range spec.FigureFields {
		if _, ok := rows[field]; !ok {
			t.Fatalf("spec.Figures holds no %s row for %s, want one", field, p)
		}
	}
	return rows
}

// secondaryPhrase is the message phrase a Figures row says sets Secondary.
func secondaryPhrase(t *testing.T) string {
	t.Helper()
	for _, f := range spec.Figures {
		if f.Message != "" {
			return f.Message
		}
	}
	t.Fatal("spec.Figures names no secondary-limit message, want one")
	return ""
}

// figureHeader is an answer header carrying every figure rows name but
// Secondary, with the remaining figure at remaining.
func figureHeader(rows map[string]spec.Figure, remaining int) http.Header {
	h := http.Header{}
	for _, field := range []string{"Pool", "Reset", "Remaining"} {
		f := rows[field]
		if f.Header == "" {
			continue
		}
		value := cmp.Or(h.Get(f.Header), f.Value)
		if field == "Remaining" {
			value = withParam(value, f.Param, strconv.Itoa(remaining))
		}
		h.Set(f.Header, value)
	}
	return h
}

// withParam is value with its param parameter set to n, or n itself where
// param is empty.
func withParam(value, param, n string) string {
	if param == "" {
		return n
	}
	parts := strings.Split(value, ";")
	for i, part := range parts {
		if key, _, ok := strings.Cut(strings.TrimSpace(part), "="); ok && key == param {
			parts[i] = param + "=" + n
		}
	}
	return strings.Join(parts, ";")
}

// paramOf is the number f's row states: its Param parameter, or its whole
// value.
func paramOf(t *testing.T, f spec.Figure) int {
	t.Helper()
	raw := f.Value
	if f.Param != "" {
		raw = ""
		for part := range strings.SplitSeq(f.Value, ";") {
			if key, value, ok := strings.Cut(strings.TrimSpace(part), "="); ok && key == f.Param {
				raw = value
			}
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("Setup: the %s %s row's value %q states no number: %v", f.Product, f.Field, f.Value, err)
	}
	return n
}

// wantPool is the pool name an answer carrying rows' figures names.
func wantPool(rows map[string]spec.Figure) string {
	if f := rows["Pool"]; f.Header != "" {
		return f.Value
	}
	return ""
}

// wantRemaining is the remaining figure an answer carrying rows' figures at
// remaining reports.
func wantRemaining(rows map[string]spec.Figure, remaining int) int {
	if rows["Remaining"].Header == "" {
		return forgeapi.BudgetRemainingUnknown
	}
	return remaining
}

// checkReset holds got to the reset an answer carrying rows' figures reports,
// that answer arriving between before and after.
func checkReset(t *testing.T, what string, rows map[string]spec.Figure, got, before, after time.Time) {
	t.Helper()
	f := rows["Reset"]
	switch {
	case f.Header == "":
		if !got.IsZero() {
			t.Errorf("%s = %v, want the zero time: %s", what, got, f.Says)
		}
	case f.Relative:
		in := time.Duration(paramOf(t, f)) * time.Second
		if got.Before(before.Add(in)) || got.After(after.Add(in)) {
			t.Errorf("%s = %v, want %v after the answer, between %v and %v: %s", what, got, in, before.Add(in), after.Add(in), f.Says)
		}
	default:
		if want := time.Unix(int64(paramOf(t, f)), 0); !got.Equal(want) {
			t.Errorf("%s = %v, want %v: %s", what, got, want, f.Says)
		}
	}
}

// figureAnswer is one figureMethod call against an answer of status, header
// and body: its error, the client, the last attempt sent, and the instants
// bracketing the call.
type figureAnswer struct {
	before, after time.Time
	err           *forgeapi.Error
	client        figureClient
	attempt       *http.Request
}

func answerFigures(t *testing.T, p spec.Product, status int, header http.Header, body string) figureAnswer {
	t.Helper()
	if e := requireEntry(t, p, figureMethod); pending(e) {
		t.Skip(pendingReason(e))
	}
	var last atomic.Pointer[http.Request]
	observe := forgeapi.WithAttemptObserver(func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			last.Store(r)
			return next.RoundTrip(r)
		})
	})
	before := time.Now()
	client, err := answeredOn(t, p, figureMethod, status, header, body, observe)
	after := time.Now()
	fc, ok := client.(figureClient)
	if !ok {
		t.Fatalf("Setup: the %s client %T reports no BudgetState and PoolOf", p, client)
	}
	if last.Load() == nil {
		t.Fatalf("%s on %s sent no attempt, want one", figureMethod, p)
	}
	return figureAnswer{before: before, after: after, err: forgeErrorOf(t, figureMethod, err), client: fc, attempt: last.Load()}
}

// TestAPrimaryLimitCarriesTheFiguresTheTableNames holds every product's spent
// budget to its Figures rows: the failed answer's Error carries the pool, the
// remaining figure and the reset the answer states, never Secondary, and the
// client's Pools and PoolOf report the same pool from it.
func TestAPrimaryLimitCarriesTheFiguresTheTableNames(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			rows := figureRows(t, p)
			a := answerFigures(t, p, http.StatusTooManyRequests, figureHeader(rows, 0), `{"message":"API rate limit exceeded"}`)
			pool := wantPool(rows)
			if a.err.Kind != forgeapi.KindRateLimited || a.err.Secondary {
				t.Errorf("%s on %s at a spent budget = kind %v, secondary %t, want %v, not secondary", figureMethod, p, a.err.Kind, a.err.Secondary, forgeapi.KindRateLimited)
			}
			if a.err.Pool != pool {
				t.Errorf("Error.Pool = %q, want %q: %s", a.err.Pool, pool, rows["Pool"].Says)
			}
			if want := wantRemaining(rows, 0); a.err.Remaining != want {
				t.Errorf("Error.Remaining = %d, want %d: %s", a.err.Remaining, want, rows["Remaining"].Says)
			}
			checkReset(t, "Error.Reset", rows, a.err.Reset, a.before, a.after)
			if got := a.client.PoolOf(a.attempt); got != pool {
				t.Errorf("PoolOf(the attempt) = %q, want %q: %s", got, pool, rows["Pool"].Says)
			}
			pools := a.client.BudgetState().Pools
			if rows["Remaining"].Header == "" {
				if len(pools) != 0 {
					t.Errorf("BudgetState().Pools = %+v, want none: %s", pools, rows["Remaining"].Says)
				}
				return
			}
			if len(pools) != 1 || pools[0].Name != pool || pools[0].Remaining != 0 {
				t.Fatalf("BudgetState().Pools = %+v, want one pool named %q at 0", pools, pool)
			}
			checkReset(t, "BudgetState().Pools[0].Reset", rows, pools[0].Reset, a.before, a.after)
		})
	}
}

// TestARetryAfterIsASecondaryLimitWhereTheTableSaysSo holds Secondary to the
// products whose Figures row names Retry-After, and elsewhere the same answer to
// the primary limit it reads as; either way Error.Pool is the pool the answer
// names.
func TestARetryAfterIsASecondaryLimitWhereTheTableSaysSo(t *testing.T) {
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			rows := figureRows(t, p)
			header := figureHeader(rows, 0)
			header.Set("Retry-After", "60")
			a := answerFigures(t, p, http.StatusTooManyRequests, header, `{"message":"API rate limit exceeded"}`)
			secondary := rows["Secondary"].Header == "Retry-After"
			pool := wantPool(rows)
			if a.err.Kind != forgeapi.KindRateLimited || a.err.Secondary != secondary || a.err.RetryAfter != time.Minute {
				t.Errorf("%s on %s answered 429 with Retry-After = kind %v, secondary %t, retry after %v, want %v, secondary %t, after %v: %s",
					figureMethod, p, a.err.Kind, a.err.Secondary, a.err.RetryAfter, forgeapi.KindRateLimited, secondary, time.Minute, rows["Secondary"].Says)
			}
			if a.err.Pool != pool {
				t.Errorf("Error.Pool = %q, want %q: %s", a.err.Pool, pool, rows["Pool"].Says)
			}
			if want := wantRemaining(rows, 0); a.err.Remaining != want {
				t.Errorf("Error.Remaining = %d, want %d: %s", a.err.Remaining, want, rows["Remaining"].Says)
			}
		})
	}
}

// TestASecondaryLimitMessageIsARateLimitWhereTheTableSaysSo holds a 403 whose
// message is the secondary limit's, with budget left and no Retry-After, to the
// Figures rows: a secondary limit where the product's row names the message,
// and elsewhere the permission refusal it reads as, each naming the answer's
// pool.
func TestASecondaryLimitMessageIsARateLimitWhereTheTableSaysSo(t *testing.T) {
	phrase := secondaryPhrase(t)
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			rows := figureRows(t, p)
			body := `{"message":"You have exceeded a ` + phrase + `. Please wait a few minutes before you try again."}`
			a := answerFigures(t, p, http.StatusForbidden, figureHeader(rows, 5), body)
			secondary := rows["Secondary"].Message == phrase
			kind, pool := forgeapi.KindForbidden, wantPool(rows)
			if secondary {
				kind = forgeapi.KindRateLimited
			}
			if a.err.Kind != kind || a.err.Secondary != secondary {
				t.Errorf("%s on %s answered 403 with the secondary-limit message = kind %v, secondary %t, want %v, secondary %t: %s",
					figureMethod, p, a.err.Kind, a.err.Secondary, kind, secondary, rows["Secondary"].Says)
			}
			if a.err.Pool != pool {
				t.Errorf("Error.Pool = %q, want %q: %s", a.err.Pool, pool, rows["Pool"].Says)
			}
			if want := wantRemaining(rows, 5); a.err.Remaining != want {
				t.Errorf("Error.Remaining = %d, want %d: %s", a.err.Remaining, want, rows["Remaining"].Says)
			}
		})
	}
}
