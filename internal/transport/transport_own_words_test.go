package transport

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/go/masked"
)

// gatewayHeader is a connection header whose value is the word given, which is
// redacted from what an instance answers.
func gatewayHeader(value string) []forgeapi.Header {
	return []forgeapi.Header{{Name: "X-Gateway-Key", Value: masked.NewSecret(value)}}
}

// TestTheLibrarysOwnWordsReachTheCallerAsWritten holds a failure this library
// writes to its own words where a connection header's value is one of them: that
// value is redacted from what an instance answers, and text the library wrote
// carries no secret to redact.
func TestTheLibrarysOwnWordsReachTheCallerAsWritten(t *testing.T) {
	for _, test := range []struct {
		run  func(t *testing.T, headers []forgeapi.Header, buf *bytes.Buffer) error
		name string
		word string
		want string
	}{
		{name: "a_downgrade_refusal", word: "https", want: "redirect hop downgrades from https to http", run: downgradeRefusal},
		{name: "the_mutation_gate", word: "mutations", want: "mutations are disabled on this client", run: mutationRefusal},
		{
			name: "a_capability_refusal", word: "instance", run: capabilityRefusal,
			want: "this instance carries no " + string(forgeapi.CapRerunChecks) + " capability",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := test.run(t, gatewayHeader(test.word), &buf)
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("the refusal = %v, want a *forgeapi.Error", err)
			}
			if fe.Message != test.want {
				t.Errorf("the refusal's message = %q, want %q", fe.Message, test.want)
			}
			carried := 0
			for _, line := range records(t, &buf) {
				said, ok := line["error"]
				if !ok {
					continue
				}
				carried++
				if said != test.want {
					t.Errorf("the line's error = %q, want %q", said, test.want)
				}
			}
			if carried == 0 {
				t.Error("no line carries the refusal, want the refusal's line to")
			}
		})
	}
}

// TestARetrysStatusReachesTheLineAsAnswered holds the status a retried attempt
// answered to its digits where a connection header's value is those digits.
func TestARetrysStatusReachesTheLineAsAnswered(t *testing.T) {
	srv, _ := answering(t, http.StatusBadGateway)
	var buf bytes.Buffer
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL, Headers: gatewayHeader("502")},
		forgeapi.WithRetries(1), forgeapi.WithLogger(recordingLogger(&buf)))
	if _, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}); err != nil {
		t.Fatalf("Do after one 502 = %v, want the retry's answer", err)
	}
	line := onlyRecord(t, &buf)
	if line["retried_on"] != "502" {
		t.Errorf("the line's retried_on = %v, want %q", line["retried_on"], "502")
	}
}

func downgradeRefusal(t *testing.T, headers []forgeapi.Header, buf *bytes.Buffer) error {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/api/v1/user", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL, Headers: headers},
		forgeapi.WithWireTransport(srv.Client().Transport), forgeapi.WithLogger(recordingLogger(buf)))
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	return err
}

func mutationRefusal(t *testing.T, headers []forgeapi.Header, buf *bytes.Buffer) error {
	t.Helper()
	srv, _ := answering(t)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL, Headers: headers},
		forgeapi.WithMutations(false), forgeapi.WithLogger(recordingLogger(buf)))
	_, err := c.Do(t.Context(), &Request{Op: "CreateLabel", Method: http.MethodPost, Path: "/labels"})
	return err
}

func capabilityRefusal(t *testing.T, headers []forgeapi.Header, buf *bytes.Buffer) error {
	t.Helper()
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example", Headers: headers},
		forgeapi.WithLogger(recordingLogger(buf)))
	return c.Refuse(t.Context(), "RerunChecks", forgeapi.CapRerunChecks, forgeapi.SupportNo,
		forgeapi.Evidence{Source: forgeapi.EvidenceMetadata, Detail: "measured"})
}
