package transport

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// drainFailure is the Debug line httpx.Drain logs through slog's default logger
// when the body it drains cannot be read, as a body already closed cannot.
const drainFailure = "failed to drain response body"

// processLog captures slog's default logger at Debug for the test. Serial: the
// default is process-wide.
func processLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestAnAnswerReadWholeIsClosedWithoutADrainFailure(t *testing.T) {
	logged := processLog(t)
	srv, _ := answering(t)
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
	read := documentRead()
	read.AbsoluteURL = srv.URL + "/graphql"
	if _, err := c.Do(t.Context(), read); err != nil {
		t.Fatalf("Do(a document read) = %v, want nil", err)
	}
	if strings.Contains(logged.String(), drainFailure) {
		t.Errorf("Do(a document read) logged %q, want nothing drained from the body it read whole and closed:\n%s", drainFailure, logged)
	}
}

func TestAnEndpointAnswerReadWholeIsClosedWithoutADrainFailure(t *testing.T) {
	logged := processLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"access_token":"t"}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	defer srv.Close()
	c, err := OpenEndpoint(&forgeapi.Connection{WebBaseURL: srv.URL}, settingsFor(
		forgeapi.WithWireTransport(&http.Transport{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithLogger(discardLogger()),
	), forgeapi.FamilyGitHub)
	if err != nil {
		t.Fatalf("Setup: OpenEndpoint(%q): %v", srv.URL, err)
	}
	t.Cleanup(c.Close)
	if _, err := c.PostForm(t.Context(), "PollDeviceGrant", "/login/oauth/access_token", url.Values{"device_code": {"d"}}); err != nil {
		t.Fatalf("PostForm = %v, want nil", err)
	}
	if strings.Contains(logged.String(), drainFailure) {
		t.Errorf("PostForm logged %q, want nothing drained from the body it read whole and closed:\n%s", drainFailure, logged)
	}
}
