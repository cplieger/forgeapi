package transport

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestAStatusARequestReadsAsAnAnswerReachesTheCallerWithItsBody holds what
// [Request.Answers] promises: a non-2xx status the request names is returned with its
// body and no error, so the family reads it as the outcome it is.
func TestAStatusARequestReadsAsAnAnswerReachesTheCallerWithItsBody(t *testing.T) {
	const body = `{"status":"pending","details":{"uuid":"u-1"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("Setup: writing the 409: %v", err)
		}
	}))
	defer srv.Close()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
	resp, err := c.Do(t.Context(), &Request{
		Op: "MergePR", Method: http.MethodPut, Path: "/merge", Answers: []int{http.StatusConflict},
	})
	if err != nil {
		t.Fatalf("Do of a request naming 409 as an answer = %v, want no error", err)
	}
	if resp.Status != http.StatusConflict || string(resp.Body) != body {
		t.Errorf("Do of a request naming 409 as an answer = status %d body %q, want %d and %q", resp.Status, resp.Body, http.StatusConflict, body)
	}
}

func TestAStatusARequestDoesNotNameIsStillARefusal(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusConflict} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
			_, err := c.Do(t.Context(), &Request{
				Op: "MergePR", Method: http.MethodPut, Path: "/merge", Answers: []int{http.StatusAccepted},
			})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("Do of a %d the request does not name = %v, want a *forgeapi.Error", status, err)
			}
			if fe.Status != status {
				t.Errorf("Do of a %d the request does not name = status %d, want %d", status, fe.Status, status)
			}
		})
	}
}
