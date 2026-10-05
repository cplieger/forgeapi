package gitea

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// movedSelector is the selector the canonical repository moved to in these cases.
const movedSelector = testOwner + "/" + testRepo + "-moved"

// reply is what one route of a moved instance answers: a redirect to location,
// path and query of the request kept, where location is set, and body otherwise.
type reply struct {
	location string
	body     string
}

// movedServer is an instance answering each "METHOD escaped-path" from replies and
// recording every request that arrived, so a case can name a write that should
// never have been sent.
type movedServer struct {
	server  *httptest.Server
	replies map[string]reply
	sent    []string
	mu      sync.Mutex
}

func newMovedServer(t *testing.T, replies map[string]reply) *movedServer {
	t.Helper()
	m := &movedServer{replies: replies}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20)); err != nil {
			t.Errorf("Setup: reading the body of %s %s: %v", r.Method, r.URL.EscapedPath(), err)
		}
		key := r.Method + " " + r.URL.EscapedPath()
		m.mu.Lock()
		m.sent = append(m.sent, key)
		answer, ok := m.replies[key]
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			answer.body = `{"message":"no route for ` + key + `"}`
		case answer.location != "":
			location := answer.location
			if r.URL.RawQuery != "" {
				location += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		if _, err := io.WriteString(w, answer.body); err != nil {
			t.Errorf("Setup: writing the answer for %s: %v", key, err)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

// arrived is every request the instance saw, in order.
func (m *movedServer) arrived() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}

// clientAt is a client of the moved instance whose connection names web as its web
// base, which is how a case puts the instance under a relative root.
func (m *movedServer) clientAt(t *testing.T, web string) *Client {
	t.Helper()
	client, err := open(&forgeapi.Connection{WebBaseURL: web}, time.Now,
		forgeapi.WithWireTransport(m.server.Client().Transport),
		forgeapi.WithCredentialSource(testCredential{}),
		forgeapi.WithPlaintextHTTP(true),
		forgeapi.WithPrivateAddresses(true),
		forgeapi.WithMutations(true),
		forgeapi.WithLogger(slog.New(slog.DiscardHandler)),
	)
	if err != nil {
		t.Fatalf("Setup: New(%q): %v", web, err)
	}
	client.maxItems = testMaxItems
	return client
}

// staleRefusal is the refusal err carries, which must be the stale reference's.
func staleRefusal(t *testing.T, call string, err error) *forgeapi.Error {
	t.Helper()
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeRepoRefStale {
		t.Fatalf("%s = error %v, want code %q", call, err, forgeapi.CodeRepoRefStale)
	}
	return fe
}

// A re-run resolves its run with a read, and a read that meets a move has found the
// run in a repository the caller did not name, so the re-run is not sent: re-issuing
// it against the successor is the caller's decision.
func TestAReRunWhoseRunReadMeetsAMoveRefusesWithTheSuccessorAndSendsNoReRun(t *testing.T) {
	runs := "/api/v1/repos/" + testSelector + "/actions/runs"
	m := newMovedServer(t, map[string]reply{
		"GET /api/v1/version":      {body: `{"version":"1.27.0+dev"}`},
		"GET /swagger.v1.json":     {body: `{"info":{"title":"Gitea API"},"paths":{"/repos/{owner}/{repo}/actions/runs/{run}/rerun-failed-jobs":{"post":{"operationId":"rerun"}}}}`},
		"GET /api/v1/settings/api": {body: `{"max_response_items":50}`},
		"GET " + runs:              {location: "/api/v1/repos/" + movedSelector + "/actions/runs"},
		"GET /api/v1/repos/" + movedSelector + "/actions/runs": {
			body: `{"total_count":1,"workflow_runs":[{"id":9,"head_sha":"` + testHeadSHA + `"}]}`,
		},
	})
	client := m.clientAt(t, m.server.URL)

	err := client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)

	fe := staleRefusal(t, "RerunFailedChecks on a moved repository", err)
	if fe.Successor == nil || fe.Successor.Selector != movedSelector {
		t.Errorf("RerunFailedChecks on a moved repository = successor %v, want %q", fe.Successor, movedSelector)
	}
	for _, request := range m.arrived() {
		if strings.HasPrefix(request, http.MethodPost) {
			t.Errorf("RerunFailedChecks on a moved repository sent %s, want no re-run", request)
		}
	}
}

// A single pull-request read answers no successor, so a move its fold meets after a
// first read that met none is refused as one met on the first read is, rather than
// answering a record and a verdict from two repositories as though nothing moved.
func TestAPullRequestReadWhoseFoldMeetsAMoveRefusesWithTheSuccessor(t *testing.T) {
	status := "/commits/" + testHeadSHA + "/status"
	m := newMovedServer(t, map[string]reply{
		"GET /api/v1/repos/" + testSelector + "/pulls/1": {body: pullRow(1)},
		"GET /api/v1/repos/" + testSelector + status:     {location: "/api/v1/repos/" + movedSelector + status},
		"GET /api/v1/repos/" + movedSelector + status:    {body: statusOK},
	})
	client := m.clientAt(t, m.server.URL)

	_, err := client.ReadPR(t.Context(), testRef(), testPR())

	fe := staleRefusal(t, "ReadPR whose fold met a move", err)
	if fe.Successor == nil || fe.Successor.Selector != movedSelector {
		t.Errorf("ReadPR whose fold met a move = successor %v, want %q", fe.Successor, movedSelector)
	}
}

// The successor is read beneath the connection's own API root, so an instance served
// under a relative root names it under that root, and a location outside the root
// names no repository on this connection, which is the stale refusal with none.
func TestAMoveUnderARelativeRootNamesTheSuccessorBeneathThatRoot(t *testing.T) {
	labels := "/first/api/v1/repos/" + testSelector + "/labels"
	for _, test := range []struct {
		name      string
		location  string
		successor string
	}{
		{name: "a_location_beneath_the_root", location: "/first/api/v1/repos/" + movedSelector + "/labels", successor: movedSelector},
		{name: "a_location_outside_the_root", location: "/api/v1/repos/" + movedSelector + "/labels"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newMovedServer(t, map[string]reply{
				"GET " + labels:        {location: test.location},
				"GET " + test.location: {body: labelBody},
			})
			client := m.clientAt(t, m.server.URL+"/first")

			page, err := client.ListLabels(t.Context(), testRef())

			if test.successor == "" {
				fe := staleRefusal(t, "ListLabels redirected to "+test.location, err)
				if fe.Successor != nil {
					t.Errorf("ListLabels redirected to %s = successor %+v, want none: the location is outside the connection's API root", test.location, *fe.Successor)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListLabels redirected to %s = error %v, want the successor's labels", test.location, err)
			}
			if page.Successor == nil || page.Successor.Selector != test.successor {
				t.Errorf("ListLabels redirected to %s = successor %v, want %q", test.location, page.Successor, test.successor)
			}
		})
	}
}

// A followed hop is the move's witness once the successor answers, so a read
// whose successor refuses it, or answers a body that does not decode, still names the
// move: it answers the stale code carrying the successor the location named, with the
// successor's own status, rather than that failure with the move left unreported.
func TestAMoveWhoseSuccessorFailsTheReadStillNamesTheSuccessor(t *testing.T) {
	labels := "/api/v1/repos/" + testSelector + "/labels"
	movedLabels := "/api/v1/repos/" + movedSelector + "/labels"
	pull := "/api/v1/repos/" + testSelector + "/pulls/1"
	for _, test := range []struct {
		replies map[string]reply
		read    func(*Client) error
		name    string
		status  int
	}{
		{
			name:    "a_list_whose_successor_refuses",
			replies: map[string]reply{"GET " + labels: {location: movedLabels}},
			read:    func(c *Client) error { _, err := c.ListLabels(t.Context(), testRef()); return err },
			status:  http.StatusNotFound,
		},
		{
			name:    "a_list_whose_successor_answers_no_decodable_body",
			replies: map[string]reply{"GET " + labels: {location: movedLabels}, "GET " + movedLabels: {body: "not a label list"}},
			read:    func(c *Client) error { _, err := c.ListLabels(t.Context(), testRef()); return err },
			status:  http.StatusOK,
		},
		{
			name:    "a_pull_request_read_whose_successor_refuses",
			replies: map[string]reply{"GET " + pull: {location: "/api/v1/repos/" + movedSelector + "/pulls/1"}},
			read:    func(c *Client) error { _, err := c.ReadPR(t.Context(), testRef(), testPR()); return err },
			status:  http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newMovedServer(t, test.replies)
			client := m.clientAt(t, m.server.URL)

			err := test.read(client)

			fe := staleRefusal(t, test.name, err)
			if fe.Successor == nil || fe.Successor.Selector != movedSelector {
				t.Errorf("%s = successor %v, want %q", test.name, fe.Successor, movedSelector)
			}
			if fe.Status != test.status {
				t.Errorf("%s = status %d, want %d, the successor's own answer", test.name, fe.Status, test.status)
			}
		})
	}
}

// A hop to the repository the read addressed, spelled in another case, is no move, so
// a refusal answered there is that refusal and names no successor.
func TestAHopToTheAddressedRepositoryWhoseAnswerFailsKeepsThatFailure(t *testing.T) {
	labels := "/api/v1/repos/" + testSelector + "/labels"
	m := newMovedServer(t, map[string]reply{
		"GET " + labels: {location: "/api/v1/repos/Example/Example/labels"},
	})
	client := m.clientAt(t, m.server.URL)

	_, err := client.ListLabels(t.Context(), testRef())

	var fe *forgeapi.Error
	switch {
	case !asForgeError(err, &fe):
		t.Fatalf("ListLabels redirected to its own repository, which refuses = error %v, want a *forgeapi.Error", err)
	case fe.Code == forgeapi.CodeRepoRefStale || fe.Successor != nil:
		t.Errorf("ListLabels redirected to its own repository, which refuses = (code %q, successor %v), want the refusal with no move", fe.Code, fe.Successor)
	case fe.Status != http.StatusNotFound:
		t.Errorf("ListLabels redirected to its own repository, which refuses = status %d, want %d", fe.Status, http.StatusNotFound)
	}
}

// A write's refused hop to the repository the write addressed, spelled in another
// case, is no rename, so the refusal the transport answered it with names no
// successor a consumer would re-point to the repository it already holds.
func TestAWriteWhoseRefusedHopNamesTheRepositoryItAddressedCarriesNoSuccessor(t *testing.T) {
	issue := "/issues/1"
	m := newMovedServer(t, map[string]reply{
		"PATCH /api/v1/repos/" + testSelector + issue: {location: "/api/v1/repos/Example/Example" + issue},
	})
	client := m.clientAt(t, m.server.URL)

	_, err := client.CloseIssue(t.Context(), testRef(), forgeapi.IssueRef{Number: 1})

	fe := staleRefusal(t, "CloseIssue redirected to its own repository", err)
	if fe.Successor != nil {
		t.Errorf("CloseIssue redirected to its own repository = successor %+v, want none", *fe.Successor)
	}
	if got := m.arrived(); len(got) != 1 {
		t.Errorf("CloseIssue redirected to its own repository sent %v, want the one write", got)
	}
}
