package github

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

// wireFunc is a wire transport answering from a function, so an answer can take
// time inside a synctest bubble, where a socket wait would not be durable.
type wireFunc func(*http.Request) (*http.Response, error)

func (f wireFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func wireAnswer(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: r,
	}
}

// wiredClient is a client whose every request goes through wire, logging JSON lines
// into logs.
func wiredClient(t *testing.T, wire http.RoundTripper, logs *bytes.Buffer) *Client {
	t.Helper()
	client, err := open(&forgeapi.Connection{WebBaseURL: "http://forge.example"}, time.Now,
		forgeapi.WithWireTransport(wire),
		forgeapi.WithCredentialSource(testCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithLogger(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	if err != nil {
		t.Fatalf("Setup: open: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// isBodyFailure reports whether err is the failure a body this library could not
// read answers.
func isBodyFailure(err error) bool {
	fe, ok := errors.AsType[*forgeapi.Error](err)
	return ok && fe.Code == forgeapi.CodeValidation && fe.Kind == forgeapi.KindUpstream && fe.Message == "response body did not decode"
}

func TestADocumentThatDoesNotDecodeIsRecordedWithItsCause(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		cause string
	}{
		{name: "an_empty_200", body: "", cause: "empty response body"},
		{name: "a_truncated_200", body: `{"data":{"search":`, cause: "decoding response: "},
		{name: "data_that_does_not_decode", body: `{"data":{"search":"a string"}}`, cause: "decoding response: "},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := recordedHarness(t, map[string]string{documentRoute: test.body}, documentRoute, &logs)
			_, err := h.client.ListMyPRs(t.Context())
			if !isBodyFailure(err) {
				t.Errorf("ListMyPRs over a document that does not decode = %v, want the validation failure of kind upstream", err)
			}
			line := lineOf(t, &logs, err)
			if got, _ := line["error"].(string); !strings.HasPrefix(got, test.cause) {
				t.Errorf("the failure line = error %q, want the decode error, starting %q", got, test.cause)
			}
			if line["content_type"] != "application/json" || line["body_bytes"] != float64(len(test.body)) {
				t.Errorf("the failure line = content_type %v, body_bytes %v, want application/json, %d",
					line["content_type"], line["body_bytes"], len(test.body))
			}
			if line["attempt"] != float64(2) || line["retried_on"] != "502" {
				t.Errorf("the failure line = attempt %v, retried_on %v, want 2, 502: the body came from the retried exchange",
					line["attempt"], line["retried_on"])
			}
		})
	}
}

func TestANullSearchIsRecordedWithTheCheckThatFailed(t *testing.T) {
	var logs bytes.Buffer
	h := recordedHarness(t, map[string]string{documentRoute: `{"data":{"search":null}}`}, documentRoute, &logs)
	_, err := h.client.ListMyPRs(t.Context())
	if !isBodyFailure(err) {
		t.Errorf("ListMyPRs over a null search = %v, want the validation failure of kind upstream", err)
	}
	if got := lineOf(t, &logs, err)["error"]; got != "search is null" {
		t.Errorf("the failure line = error %v, want the check that failed, search is null", got)
	}
}

func TestADecodeFailureReadsTheTimeOfTheRequestItFollows(t *testing.T) {
	t.Run("the_merge_after_a_read", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			wire := wireFunc(func(r *http.Request) (*http.Response, error) {
				switch r.Method + " " + r.URL.EscapedPath() {
				case pullsRoute:
					time.Sleep(2 * time.Second)
					return wireAnswer(r, http.StatusOK, autoMergePull("clean")), nil
				case mergeRoute:
					time.Sleep(3 * time.Second)
					return wireAnswer(r, http.StatusAccepted, `{"state":`), nil
				}
				return wireAnswer(r, http.StatusInternalServerError, `{"message":"no route"}`), nil
			})
			var logs bytes.Buffer
			client := wiredClient(t, wire, &logs)
			_, err := client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA, AutoMerge: true})
			if !isBodyFailure(err) {
				t.Errorf("MergePR over a merge answer that does not decode = %v, want the validation failure of kind upstream", err)
			}
			line := lineOf(t, &logs, err)
			if line["duration_ms"] != float64(3000) || line["op_elapsed_ms"] != float64(5000) {
				t.Errorf("the failure line = duration_ms %v, op_elapsed_ms %v, want 3000 (the merge PUT) and 5000 (the operation)",
					line["duration_ms"], line["op_elapsed_ms"])
			}
		})
	})
	t.Run("the_envelope_of_a_document", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			wire := wireFunc(func(r *http.Request) (*http.Response, error) {
				time.Sleep(time.Second)
				return wireAnswer(r, http.StatusOK, ""), nil
			})
			var logs bytes.Buffer
			client := wiredClient(t, wire, &logs)
			_, err := client.ListMyPRs(t.Context())
			if ms, _ := lineOf(t, &logs, err)["duration_ms"].(float64); ms <= 0 {
				t.Errorf("the failure line after an empty document answer = duration_ms %v, want the request's own time, above 0", ms)
			}
		})
	})
}
