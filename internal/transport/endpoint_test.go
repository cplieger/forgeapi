package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/cplieger/forgeapi"
)

// An OAuth endpoint answers its outcomes in the body, at 400 on some products
// (RFC 6749 section 5.2).
func TestAnOAuthEndpointsAnswerReachesTheCallerWhateverItsStatus(t *testing.T) {
	const body = `{"error":"authorization_pending"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if _, err := io.WriteString(w, body); err != nil {
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
	resp, err := c.PostForm(t.Context(), "PollDeviceGrant", "/login/oauth/access_token", url.Values{"device_code": {"d"}})
	if err != nil {
		t.Fatalf("PostForm to an endpoint answering 400 = %v, want no error: the outcome is in the body", err)
	}
	if resp.Status != http.StatusBadRequest || string(resp.Body) != body {
		t.Errorf("PostForm to an endpoint answering 400 = status %d body %q, want %d and %q", resp.Status, resp.Body, http.StatusBadRequest, body)
	}
}
