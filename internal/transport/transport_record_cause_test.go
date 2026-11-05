package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2"
)

// refusingThenAnswering answers its first refusals requests with a 502 and every
// later one with an empty object.
func refusingThenAnswering(t *testing.T, refusals int64) *httptest.Server {
	t.Helper()
	seen := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if seen.Add(1) <= refusals {
			w.WriteHeader(http.StatusBadGateway)
		}
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestARecordCarriesTheCauseOfItsFailureAndOfItsRetry(t *testing.T) {
	t.Run("a_refused_request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		}))
		defer srv.Close()
		var buf bytes.Buffer
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithLogger(recordingLogger(&buf)))
		_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
		var fe *forgeapi.Error
		if !asError(err, &fe) {
			t.Fatalf("a read answered 404 = %v, want a *forgeapi.Error", err)
		}
		line := onlyRecord(t, &buf)
		if got := line["error"]; got != "Not Found" || got != fe.Message {
			t.Errorf("the record of a 404 = error %v, want the returned message %q, which quotes the instance's Not Found", got, fe.Message)
		}
		if got, ok := line["retried_on"]; ok {
			t.Errorf("the record of a request never retried = retried_on %v, want the attribute absent", got)
		}
	})
	t.Run("an_answer_at_the_first_attempt", func(t *testing.T) {
		srv := refusingThenAnswering(t, 0)
		var buf bytes.Buffer
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithLogger(recordingLogger(&buf)))
		if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
			t.Fatalf("a read = %v, want nil", err)
		}
		line := onlyRecord(t, &buf)
		for _, key := range []string{"error", "retried_on"} {
			if got, ok := line[key]; ok {
				t.Errorf("the record of an answer never retried = %s %v, want the attribute absent", key, got)
			}
		}
		if got := line["level"]; got != "DEBUG" {
			t.Errorf("the record of an answer never retried = level %v, want DEBUG", got)
		}
	})
	t.Run("an_answer_after_one_retry", func(t *testing.T) {
		srv := refusingThenAnswering(t, 1)
		var buf bytes.Buffer
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(2), forgeapi.WithLogger(recordingLogger(&buf)))
		if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
			t.Fatalf("a read answered after one 502 = %v, want nil", err)
		}
		line := onlyRecord(t, &buf)
		if line["level"] != "INFO" || line["attempt"] != float64(2) || line["retried_on"] != "502" {
			t.Errorf("the record of an answer after one 502 = level %v, attempt %v, retried_on %v, want INFO, 2, 502",
				line["level"], line["attempt"], line["retried_on"])
		}
		if got, ok := line["error"]; ok {
			t.Errorf("the record of an answer after one 502 = error %v, want the attribute absent", got)
		}
	})
}

func TestADeadlineThatCutsARetryIsRecordedBesideTheCauseOfTheRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := unroutable(syscall.EHOSTUNREACH)
		seen := &atomic.Int64{}
		wire := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if seen.Add(1) == 1 {
				return nil, first
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})
		var buf bytes.Buffer
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithWireTransport(wire),
			forgeapi.WithRetries(1), forgeapi.WithOperationTimeout(20*time.Second), forgeapi.WithLogger(recordingLogger(&buf)))
		_, err := c.Do(t.Context(), &Request{Op: "ListRuns", Method: http.MethodGet, Path: "/runs"})
		// Identity, not errors.Is: the contract is the sentinel unwrapped.
		if err != context.DeadlineExceeded || errors.As(err, new(*forgeapi.Error)) {
			t.Errorf("a retry the operation deadline cut = %v, want the bare deadline sentinel", err)
		}
		line := onlyRecord(t, &buf)
		if line["attempt"] != float64(2) || line["error"] != context.DeadlineExceeded.Error() || line["retried_on"] != first.Error() {
			t.Errorf("the record of a retry the deadline cut = attempt %v, error %v, retried_on %v, want 2, %q, %q",
				line["attempt"], line["error"], line["retried_on"], context.DeadlineExceeded.Error(), first.Error())
		}
	})
}

func TestARecordNeverCarriesTheURLATransportErrorNames(t *testing.T) {
	const secret = "hunter2"
	named := "http://user:" + secret + "@forge.example/api/v1/user?private_token=" + secret
	for _, test := range []struct {
		name   string
		target string
		err    error
	}{
		{name: "the_url_the_client_names", target: named, err: unroutable(syscall.EHOSTUNREACH)},
		{name: "a_url_a_wrapping_wire_names", err: &url.Error{Op: "Get", URL: named, Err: unroutable(syscall.EHOSTUNREACH)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				wire := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.err })
				var buf bytes.Buffer
				c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithWireTransport(wire),
					forgeapi.WithRetries(1), forgeapi.WithOperationTimeout(time.Hour), forgeapi.WithLogger(recordingLogger(&buf)))
				_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", AbsoluteURL: test.target})
				var fe *forgeapi.Error
				if !asError(err, &fe) || strings.Contains(fe.Message, secret) || strings.Contains(fe.Message, "private_token") {
					t.Errorf("a transport failure naming a URL = %v, want a failure whose message leaves the URL out", err)
				}
				line := onlyRecord(t, &buf)
				for _, key := range []string{"error", "retried_on"} {
					got, _ := line[key].(string)
					if got == "" {
						t.Errorf("the record of a retried transport failure = %s %q, want the cause", key, got)
					}
					if strings.Contains(got, secret) || strings.Contains(got, "private_token") || strings.Contains(got, "user:") {
						t.Errorf("the record of a transport failure naming a URL = %s %q, want the URL left out", key, got)
					}
				}
			})
		})
	}
}

func TestARefusalThatSentNothingNamesNoAttempt(t *testing.T) {
	t.Run("with_no_request_before_it", func(t *testing.T) {
		var buf bytes.Buffer
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithLogger(recordingLogger(&buf)))
		fe := c.Refuse(t.Context(), "RerunFailedChecks", forgeapi.CapRerunChecks, forgeapi.SupportNo, forgeapi.Evidence{})
		line := onlyRecord(t, &buf)
		if got, ok := line["attempt"]; ok {
			t.Errorf("the record of a capability refusal = attempt %v, want the attribute absent: nothing was sent", got)
		}
		if got := line["error"]; got != fe.Message {
			t.Errorf("the record of a capability refusal = error %v, want the returned message %q", got, fe.Message)
		}
	})
	t.Run("after_a_retried_request_of_its_operation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			seen := &atomic.Int64{}
			wire := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				time.Sleep(700 * time.Millisecond)
				status := http.StatusOK
				if seen.Add(1) == 1 {
					status = http.StatusBadGateway
				}
				return &http.Response{
					StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{}`)), Request: r,
				}, nil
			})
			var buf bytes.Buffer
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithWireTransport(wire),
				forgeapi.WithRetries(2), forgeapi.WithOperationTimeout(time.Hour), forgeapi.WithLogger(recordingLogger(&buf)))
			ctx := CallWrite(t.Context(), "RerunFailedChecks")
			if _, err := c.Do(Call(ctx, "ConnectionCaps"), &Request{Op: "ConnectionCaps", Method: http.MethodGet, Path: "/version"}); err != nil {
				t.Fatalf("the capability read, answered after one 502 = %v, want nil", err)
			}
			c.Refuse(ctx, "RerunFailedChecks", forgeapi.CapRerunChecks, forgeapi.SupportNo, forgeapi.Evidence{})
			lines := records(t, &buf)
			if len(lines) != 2 {
				t.Fatalf("the operation recorded %d line(s), want 2 (the read, the refusal): %v", len(lines), lines)
			}
			line := lines[1]
			for _, key := range []string{"attempt", "retried_on"} {
				if got, ok := line[key]; ok {
					t.Errorf("the record of a refusal after a retried read = %s %v, want the attribute absent: the refusal sent nothing", key, got)
				}
			}
			if got := line["duration_ms"]; got != float64(0) {
				t.Errorf("the record of a refusal after a retried read = duration_ms %v, want 0: the refusal sent nothing", got)
			}
		})
	})
}

func TestAFamilyFailureReadsTheLastRequestOfItsOperation(t *testing.T) {
	srv := refusingThenAnswering(t, 1)
	var buf bytes.Buffer
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(2), forgeapi.WithLogger(recordingLogger(&buf)))
	ctx := Call(t.Context(), "ListThings")
	if _, err := c.Do(ctx, &Request{Op: "ListThings", Method: http.MethodGet, Path: "/things"}); err != nil {
		t.Fatalf("the first request, answered after one 502 = %v, want nil", err)
	}
	c.Fail(ctx, "ListThings", REST(http.MethodGet), forgeapi.CodeValidation, forgeapi.KindUpstream, http.StatusOK, "rows are null")
	if _, err := c.Do(ctx, &Request{Op: "ListThings", Method: http.MethodGet, Path: "/things", Page: 2}); err != nil {
		t.Fatalf("the second request = %v, want nil", err)
	}
	c.FailBody(ctx, "ListThings", REST(http.MethodGet), http.StatusOK, errors.New("rows are null"))
	lines := records(t, &buf)
	if len(lines) != 4 {
		t.Fatalf("the operation recorded %d line(s), want 4 (two requests, two failures): %v", len(lines), lines)
	}
	for _, test := range []struct {
		name      string
		line      map[string]any
		level     string
		attempt   float64
		retriedOn string
	}{
		{name: "the_failure_after_the_retried_request", line: lines[1], level: "WARN", attempt: 2, retriedOn: "502"},
		{name: "the_second_request", line: lines[2], level: "DEBUG", attempt: 1},
		{name: "the_failure_after_the_second_request", line: lines[3], level: "WARN", attempt: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _ := test.line["retried_on"].(string)
			if test.line["level"] != test.level || test.line["attempt"] != test.attempt || got != test.retriedOn {
				t.Errorf("the record = level %v, attempt %v, retried_on %q, want %s, %v, %q",
					test.line["level"], test.line["attempt"], got, test.level, test.attempt, test.retriedOn)
			}
			if _, ok := test.line["op_elapsed_ms"]; ok != (test.level == "WARN") {
				t.Errorf("the record = op_elapsed_ms present %v, want it on the family's failure lines only", ok)
			}
		})
	}
}
