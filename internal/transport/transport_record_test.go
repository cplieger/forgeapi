package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cplieger/forgeapi"
)

func recordingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func onlyRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(buf)
	for {
		var line map[string]any
		if err := dec.Decode(&line); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("Setup: decoding a recorded line: %v", err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 1 {
		t.Fatalf("the injected logger holds %d line(s), want the one per-request record: %v", len(lines), lines)
	}
	return lines[0]
}

func TestTheRecordNamesTheAttemptARequestReached(t *testing.T) {
	for _, test := range []struct {
		name     string
		refusals int64
		want     float64
	}{
		{name: "answered_at_the_first_attempt", want: 1},
		{name: "answered_after_one_retry", refusals: 1, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := &atomic.Int64{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if seen.Add(1) <= test.refusals {
					w.WriteHeader(http.StatusBadGateway)
				}
				if _, err := io.WriteString(w, `{}`); err != nil {
					t.Errorf("Setup: writing the answer: %v", err)
				}
			}))
			defer srv.Close()
			var buf bytes.Buffer
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(2), forgeapi.WithLogger(recordingLogger(&buf)))
			if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
				t.Fatalf("a read answered after %d refusal(s) = %v, want nil", test.refusals, err)
			}
			if got := onlyRecord(t, &buf)["attempt"]; got != test.want {
				t.Errorf("the record of a read answered after %d refusal(s) = attempt %v, want %v", test.refusals, got, test.want)
			}
		})
	}
}

func TestTheRecordDescribesTheStatusTheInstanceAnswered(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		level  string
		cache  string
	}{
		{name: "an_answer", status: http.StatusOK, level: "DEBUG", cache: "miss"},
		{name: "a_not_modified_answer", status: http.StatusNotModified, level: "DEBUG", cache: "not_modified"},
		{name: "the_first_client_error", status: http.StatusBadRequest, level: "WARN", cache: "miss"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
			}))
			defer srv.Close()
			var buf bytes.Buffer
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithLogger(recordingLogger(&buf)))
			_, _ = c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			line := onlyRecord(t, &buf)
			if got := line["level"]; got != test.level {
				t.Errorf("the record of a %d = level %v, want %s", test.status, got, test.level)
			}
			if got := line["cache"]; got != test.cache {
				t.Errorf("the record of a %d = cache %v, want %s", test.status, got, test.cache)
			}
		})
	}
}

func TestTheRecordCarriesTheRequestIDTheInstanceNamed(t *testing.T) {
	const id = "C0DE:1:2:3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-GitHub-Request-Id", id)
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	var buf bytes.Buffer
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithLogger(recordingLogger(&buf)))
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("a read = %v, want nil", err)
	}
	if got := onlyRecord(t, &buf)["upstream_request_id"]; got != id {
		t.Errorf("the record of an answer naming request id %q = upstream_request_id %v, want it carried", id, got)
	}
}
