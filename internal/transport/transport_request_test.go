package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/cplieger/forgeapi"
)

type seenRequest struct {
	target      string
	body        string
	contentType string
	mu          sync.Mutex
}

func (s *seenRequest) see(r *http.Request, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target, s.body, s.contentType = r.RequestURI, body, r.Header.Get("Content-Type")
}

func (s *seenRequest) read() (target, body, contentType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.target, s.body, s.contentType
}

func recordingInstance(t *testing.T) (*httptest.Server, *seenRequest) {
	t.Helper()
	seen := &seenRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.see(r, readBody(t, r))
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{}`); err != nil {
			t.Errorf("Setup: writing the answer: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestARequestReachesTheRouteItNames(t *testing.T) {
	for _, test := range []struct {
		name string
		req  Request
		want string
	}{
		{name: "a_path", req: Request{Path: "/repos/o/n"}, want: "/api/v1/repos/o/n"},
		{name: "a_path_and_a_query", req: Request{Path: "/repos/o/n/labels", Query: url.Values{"page": {"2"}}}, want: "/api/v1/repos/o/n/labels?page=2"},
		{name: "an_escaped_path", req: Request{EscapedPath: "/projects/group%2Fsub"}, want: "/api/v1/projects/group%2Fsub"},
		{name: "an_escaped_path_and_a_query", req: Request{EscapedPath: "/projects/group%2Fsub", Query: url.Values{"per_page": {"2"}}}, want: "/api/v1/projects/group%2Fsub?per_page=2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, seen := recordingInstance(t)
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL})
			req := test.req
			req.Op, req.Method = "ListLabels", http.MethodGet
			if _, err := c.Do(t.Context(), &req); err != nil {
				t.Fatalf("Do(%+v) = %v, want nil", test.req, err)
			}
			if got, _, _ := seen.read(); got != test.want {
				t.Errorf("Do(%+v) reached %q, want %q", test.req, got, test.want)
			}
		})
	}
}

func TestARequestCarriesAJSONBodyExactlyWhenItHasOne(t *testing.T) {
	for _, test := range []struct {
		name        string
		req         Request
		body        string
		contentType string
	}{
		{name: "a_read", req: Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"}},
		{
			name:        "a_mutation",
			req:         Request{Op: "CreateIssue", Method: http.MethodPost, Path: "/x", Body: map[string]any{"title": "<t>"}},
			body:        `{"title":"<t>"}`,
			contentType: "application/json",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, seen := recordingInstance(t)
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: srv.URL}, forgeapi.WithMutations(true))
			req := test.req
			if _, err := c.Do(t.Context(), &req); err != nil {
				t.Fatalf("Do(%s) = %v, want nil", test.req.Op, err)
			}
			_, body, contentType := seen.read()
			if body != test.body {
				t.Errorf("Do(%s) sent body %q, want %q", test.req.Op, body, test.body)
			}
			if contentType != test.contentType {
				t.Errorf("Do(%s) sent Content-Type %q, want %q", test.req.Op, contentType, test.contentType)
			}
		})
	}
}
