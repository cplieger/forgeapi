package github

import (
	"cmp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// TestEveryDocumentThrottleTheSpecNamesIsARateLimit drives each document
// rate-limit answer the expectation table names for this product end to end: an
// envelope carrying errors and no data is the limit, with the answer's own figures,
// and it leaves the connection on documents, since a throttle is not the document's.
func TestEveryDocumentThrottleTheSpecNamesIsARateLimit(t *testing.T) {
	for _, th := range spec.Throttles {
		if th.Product != spec.GitHub || !th.Document {
			continue
		}
		t.Run(strconv.Itoa(th.Status)+"_"+th.Header+"_"+strings.ReplaceAll(th.Message, " ", "_"), func(t *testing.T) {
			header := headerPairs("X-RateLimit-Resource", "graphql", "X-RateLimit-Remaining", "4000", "X-RateLimit-Reset", "1790000000")
			if th.Header != "" {
				header.Set(th.Header, cmp.Or(th.Value, "60"))
			}
			message := "API rate limit already exceeded for user ID 1."
			if th.Message != "" {
				message = "You have exceeded a " + th.Message + ". Please wait a few minutes before you try again."
			}
			h := newHarness(t, map[string]string{documentRoute: `{"errors":[{"type":"RATE_LIMITED","message":"` + message + `"}]}`})
			h.instance.status(documentRoute, th.Status)
			h.instance.answerHeaders(documentRoute, header)

			_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("ReadPR against %s = %v, want a *forgeapi.Error", th.Says, err)
			}
			secondary := th.Message != "" || th.Header == "Retry-After"
			if fe.Kind != forgeapi.KindRateLimited || fe.Status != th.Status || fe.Secondary != secondary {
				t.Errorf("ReadPR = kind %v status %d secondary %t, want %v, %d, %t: %s",
					fe.Kind, fe.Status, fe.Secondary, forgeapi.KindRateLimited, th.Status, secondary, th.Says)
			}
			remaining, _ := strconv.Atoi(header.Get("X-RateLimit-Remaining"))
			if fe.Pool != "graphql" || fe.Remaining != remaining || !fe.Reset.Equal(time.Unix(1790000000, 0)) {
				t.Errorf("ReadPR = pool %q remaining %d reset %v, want the answer's graphql, %d and its reset", fe.Pool, fe.Remaining, fe.Reset, remaining)
			}
			wantWait := time.Duration(0)
			if th.Header == "Retry-After" {
				wantWait = time.Minute
			}
			if fe.RetryAfter != wantWait {
				t.Errorf("ReadPR = retry after %v, want %v", fe.RetryAfter, wantWait)
			}
			if h.client.isDegraded() {
				t.Error("a document rate limit degraded the connection, want it on documents")
			}
			if n := h.spy.times("RateLimited"); n != 1 {
				t.Errorf("the rate-limit counter fired %d time(s), want 1", n)
			}
		})
	}
}

// A refused document whose answer states budget left and no secondary signal is the
// document's own failure, as before: the limit reading takes only the answer's signs.
func TestADocumentRefusedWithBudgetLeftIsNoRateLimit(t *testing.T) {
	h := newHarness(t, map[string]string{
		documentRoute: `{"errors":[{"type":"UNPROCESSABLE","message":"Field 'teleported' doesn't exist"}]}`,
	})
	h.instance.answerHeaders(documentRoute, headerPairs("X-RateLimit-Resource", "graphql", "X-RateLimit-Remaining", "4000"))
	_, err := h.client.ReadPR(t.Context(), testRef(), testPR())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Kind != forgeapi.KindUpstream {
		t.Fatalf("ReadPR against a refused document = %v, want an upstream failure", err)
	}
	if !h.client.isDegraded() {
		t.Error("a refused document left the connection on documents, want it degraded")
	}
}
