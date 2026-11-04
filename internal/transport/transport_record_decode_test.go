package transport

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi"
)

// refusingThenServing answers its first request with a 502 and every later one with
// body, as JSON.
func refusingThenServing(t *testing.T, body string) *httptest.Server {
	t.Helper()
	seen := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if seen.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnAnswerIntoThatDoesNotDecodeIsRecordedOnceWithItsCause(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		cause string
	}{
		{name: "an_empty_200", body: "", cause: "empty response body"},
		{name: "a_truncated_200", body: `{"name":`, cause: "decoding response: "},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := refusingThenServing(t, test.body)
			var buf bytes.Buffer
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithRetries(2), forgeapi.WithLogger(recordingLogger(&buf)))
			var into struct {
				Name string `json:"name"`
			}
			_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Into: &into})
			var fe *forgeapi.Error
			if !asError(err, &fe) || fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream || fe.Message != "response body did not decode" {
				t.Fatalf("a 200 that does not decode = %v, want the validation failure of kind upstream", err)
			}
			line := onlyRecord(t, &buf)
			if got, _ := line["error"].(string); !strings.HasPrefix(got, test.cause) {
				t.Errorf("the record = error %q, want the decode error, starting %q", got, test.cause)
			}
			if line["content_type"] != "application/json" || line["body_bytes"] != float64(len(test.body)) {
				t.Errorf("the record = content_type %v, body_bytes %v, want application/json, %d",
					line["content_type"], line["body_bytes"], len(test.body))
			}
			if line["level"] != "WARN" || line["status"] != float64(http.StatusOK) || line["diag_id"] != fe.DiagID {
				t.Errorf("the record = level %v, status %v, diag_id %v, want WARN, 200, %s", line["level"], line["status"], line["diag_id"], fe.DiagID)
			}
			if line["attempt"] != float64(2) || line["retried_on"] != "502" {
				t.Errorf("the record = attempt %v, retried_on %v, want 2, 502: the body came from the retried exchange",
					line["attempt"], line["retried_on"])
			}
		})
	}
}

func TestADecodeFailureRedactsTheCredentialItQuotes(t *testing.T) {
	token := ("github_pat_" + strings.Repeat("A1b2c3D4", 12))[:93]
	synctest.Test(t, func(t *testing.T) {
		wire := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			time.Sleep(time.Second)
			return jsonAnswer(r, http.StatusOK, `{"at":"`+token+`"}`), nil
		})
		var buf bytes.Buffer
		c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithWireTransport(wire),
			forgeapi.WithCredentialSource(heldCredential(token)), forgeapi.WithLogger(recordingLogger(&buf)))
		var into struct {
			At time.Time `json:"at"`
		}
		if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user", Into: &into}); err == nil {
			t.Fatal("a time field holding the credential = nil, want the decode failure")
		}
		line := onlyRecord(t, &buf)
		got, _ := line["error"].(string)
		if !strings.Contains(got, "[redacted]") || strings.Contains(got, token[:64]) {
			t.Errorf("the record = error %q, want the credential redacted whole", got)
		}
		if ms, _ := line["duration_ms"].(float64); ms <= 0 {
			t.Errorf("the record = duration_ms %v, want the exchange's own time, above 0", line["duration_ms"])
		}
	})
}

func TestAFailDecodeWithNoCauseNamesTheMissingCause(t *testing.T) {
	var buf bytes.Buffer
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithLogger(recordingLogger(&buf)))
	resp := &Response{Status: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}}, Body: []byte("<html>")}
	fe := c.FailDecode(Call(t.Context(), "ListThings"), "ListThings", REST(http.MethodGet), resp, nil)
	if fe.Code != forgeapi.CodeValidation || fe.Kind != forgeapi.KindUpstream || fe.Status != http.StatusOK {
		t.Errorf("FailDecode = code %q, kind %v, status %d, want validation, upstream, 200", fe.Code, fe.Kind, fe.Status)
	}
	line := onlyRecord(t, &buf)
	if line["error"] != "decode error missing" {
		t.Errorf("the record = error %v, want decode error missing", line["error"])
	}
	if line["content_type"] != "text/html" || line["body_bytes"] != float64(len("<html>")) {
		t.Errorf("the record = content_type %v, body_bytes %v, want text/html, 6", line["content_type"], line["body_bytes"])
	}
}

func TestABodyFailureCarriesTheCheckThatFailed(t *testing.T) {
	var buf bytes.Buffer
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithLogger(recordingLogger(&buf)))
	check := errors.New("rows are null")
	fe := c.FailBody(Call(t.Context(), "ListThings"), "ListThings", REST(http.MethodGet), http.StatusOK, check)
	if fe.Message != "response body did not decode" {
		t.Errorf("FailBody = message %q, want response body did not decode", fe.Message)
	}
	line := onlyRecord(t, &buf)
	if line["error"] != check.Error() {
		t.Errorf("the record = error %v, want the check %q", line["error"], check.Error())
	}
	for _, key := range []string{"content_type", "body_bytes"} {
		if got, ok := line[key]; ok {
			t.Errorf("the record = %s %v, want the attribute absent: the body decoded", key, got)
		}
	}
}
