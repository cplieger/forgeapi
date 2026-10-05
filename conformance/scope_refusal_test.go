package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// fineGrainedRefusal is gitlab.com's answer to a fine-grained token that lacks the
// permission an operation needs, as recorded on 2026-10-01 on the viewer's merge
// requests, with the permission it names left as a parameter.
func fineGrainedRefusal(permission string) string {
	return `{"error":"insufficient_granular_scope","error_description":"Access denied: This operation requires a fine-grained personal access token with the following user permissions: ` + permission + `."}`
}

// mergeRequestRead is the permission the captured refusal names.
const mergeRequestRead = "[Merge Request: Read]"

// gitlabRESTReads are GitLab operations whose one request is a REST route, so a
// refusal of that route is the answer the operation reads.
var gitlabRESTReads = []string{
	"Identity.Whoami",
	"Repos.ListRepos",
	"PullRequests.ListMyPRs",
	"Issues.ListMyIssues",
	"Capabilities.RepoAffordances",
}

// refusedOn drives one operation against a server that answers every request with
// one status and body, and answers the operation's error.
func refusedOn(t *testing.T, p spec.Product, method string, status int, body string) error {
	t.Helper()
	op, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", method)
	}
	srv, _ := newAnswerServer(t, requireEntry(t, p, method), status, body)
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(p)}
	client, err := guard(func() (any, error) { return newClient(p, conn, offlineOptions(srv)...) })
	if err != nil {
		t.Fatalf("Setup: %s client: %v", p, err)
	}
	_, err = guard(func() (any, error) { return op.invoke(t.Context(), client, canonicalSubject(p)) })
	return err
}

// forgeErrorOf is the *forgeapi.Error an operation answered, failing the case for
// any other answer.
func forgeErrorOf(t *testing.T, method string, err error) *forgeapi.Error {
	t.Helper()
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("%s = error %v, want a *forgeapi.Error", method, err)
	}
	return fe
}

// checkScopeInsufficient holds a refusal to a permission refusal of the operation
// that met it, carrying the text that names what the credential lacks.
func checkScopeInsufficient(t *testing.T, method string, fe *forgeapi.Error, names string) {
	t.Helper()
	if fe.Kind != forgeapi.KindForbidden || fe.Code != "scope_insufficient" {
		t.Errorf("%s on GitLab refused for a missing permission = (%v, %q), want (%v, %q): the instance authenticated the credential and refused the operation",
			method, fe.Kind, fe.Code, forgeapi.KindForbidden, "scope_insufficient")
	}
	if fe.Retryable {
		t.Errorf("%s on GitLab refused for a missing permission is retryable, want not: the remedy is the credential's permission set", method)
	}
	if fe.Status != http.StatusForbidden || fe.Family != forgeapi.FamilyGitLab {
		t.Errorf("%s on GitLab refused for a missing permission = (status %d, family %v), want (%d, %v)", method, fe.Status, fe.Family, http.StatusForbidden, forgeapi.FamilyGitLab)
	}
	if _, name, _ := strings.Cut(method, "."); !strings.Contains(fe.Op, name) {
		t.Errorf("%s on GitLab refused for a missing permission = Op %q, want the role method's own spelling", method, fe.Op)
	}
	if fe.DiagID == "" {
		t.Errorf("%s on GitLab refused for a missing permission = empty DiagID, want one: the operation failed", method)
	}
	if !strings.Contains(fe.Message, names) {
		t.Errorf("%s on GitLab refused for a missing permission = message %q, want it to carry %q, the text naming what the credential lacks", method, fe.Message, names)
	}
}

func TestAGitLabFineGrainedRefusalNamesThePermissionTheCredentialLacks(t *testing.T) {
	for _, method := range gitlabRESTReads {
		t.Run(method, func(t *testing.T) {
			err := refusedOn(t, spec.GitLab, method, http.StatusForbidden, fineGrainedRefusal(mergeRequestRead))
			checkScopeInsufficient(t, method, forgeErrorOf(t, method, err), mergeRequestRead)
		})
	}
}

// A legacy token missing a scope is answered with RFC 6750's own error and the
// scope it needs, and the remedy is the same: the credential's scope set.
func TestAGitLabLegacyScopeRefusalNamesTheScopeTheCredentialLacks(t *testing.T) {
	const body = `{"error":"insufficient_scope","error_description":"The request requires higher privileges than provided by the access token.","scope":"read_api api"}`
	err := refusedOn(t, spec.GitLab, "Identity.Whoami", http.StatusForbidden, body)
	fe := forgeErrorOf(t, "Identity.Whoami", err)
	checkScopeInsufficient(t, "Identity.Whoami", fe, "higher privileges")
	if !strings.Contains(fe.Message, "read_api") {
		t.Errorf("Whoami on GitLab refused for a missing scope = message %q, want it to carry the scope member %q", fe.Message, "read_api api")
	}
}

// The body's error member decides the code, so a refusal without either scope
// error stays the plain forbidden a role refusal answers, whatever its prose says.
func TestAGitLabForbiddenWithoutAScopeErrorCarriesNoCode(t *testing.T) {
	bodies := map[string]string{
		"message_only":      `{"message":"403 Forbidden"}`,
		"other_error":       `{"error":"forbidden","error_description":"This operation requires a fine-grained personal access token with the following user permissions: [Merge Request: Read]."}`,
		"description_alone": `{"error_description":"Access denied: insufficient_granular_scope"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			err := refusedOn(t, spec.GitLab, "PullRequests.ListMyPRs", http.StatusForbidden, body)
			fe := forgeErrorOf(t, "PullRequests.ListMyPRs", err)
			if fe.Kind != forgeapi.KindForbidden || fe.Code != "" {
				t.Errorf("ListMyPRs on GitLab answered 403 %s = (%v, %q), want (%v, no code)", body, fe.Kind, fe.Code, forgeapi.KindForbidden)
			}
		})
	}
}

// The description is the instance's text, so it reaches the caller sanitized and
// bounded as every upstream message does.
func TestAGitLabScopeRefusalMessageIsSanitizedAndBounded(t *testing.T) {
	description := "Access denied: \x1b[31mthe following user permissions: [Project: Read]\u202e." + strings.Repeat(" padding", 1<<13)
	raw, err := json.Marshal(map[string]string{"error": "insufficient_granular_scope", "error_description": description})
	if err != nil {
		t.Fatalf("Setup: encoding the refusal: %v", err)
	}
	fe := forgeErrorOf(t, "Identity.Whoami", refusedOn(t, spec.GitLab, "Identity.Whoami", http.StatusForbidden, string(raw)))

	if !strings.Contains(fe.Message, "[Project: Read]") {
		t.Errorf("the refusal's message = %q, want it to carry %q", fe.Message, "[Project: Read]")
	}
	if strings.ContainsRune(fe.Message, '\x1b') || strings.ContainsRune(fe.Message, '\u202e') {
		t.Errorf("the refusal's message = %q, want no terminal escape and no bidirectional override from the instance's text", fe.Message)
	}
	if len(fe.Message) >= len(description) {
		t.Errorf("the refusal's message is %d byte(s) for a %d-byte description, want it bounded", len(fe.Message), len(description))
	}
}

// The message a scope refusal carries is the text naming what the credential lacks,
// so a generic message member beside it, an object in its place, or a description
// long enough to reach the bound never costs the caller that text or the code.
func TestAGitLabScopeRefusalKeepsWhatTheCredentialLacksWhateverElseTheBodySays(t *testing.T) {
	longDescription := "The request requires higher privileges than provided by the access token." + strings.Repeat(" padding", 1<<8)
	for _, test := range []struct {
		name  string
		body  string
		names []string
	}{
		{
			name:  "beside_a_generic_message",
			body:  `{"message":"403 Forbidden","error":"insufficient_granular_scope","error_description":"Access denied: This operation requires a fine-grained personal access token with the following user permissions: [Merge Request: Read]."}`,
			names: []string{mergeRequestRead},
		},
		{
			name:  "beside_a_message_object",
			body:  `{"message":{"base":["Identity verification is required in order to run CI jobs"]},"error":"insufficient_scope","error_description":"The request requires higher privileges than provided by the access token.","scope":"api"}`,
			names: []string{"higher privileges", "api"},
		},
		{
			name:  "where_no_description_came",
			body:  `{"message":"Access denied: this operation requires the permissions [Merge Request: Read].","error":"insufficient_granular_scope"}`,
			names: []string{mergeRequestRead},
		},
		{
			name:  "behind_a_description_past_the_bound",
			body:  `{"error":"insufficient_scope","error_description":"` + longDescription + `","scope":"read_api"}`,
			names: []string{"read_api"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := forgeErrorOf(t, "Identity.Whoami", refusedOn(t, spec.GitLab, "Identity.Whoami", http.StatusForbidden, test.body))
			for _, names := range test.names {
				checkScopeInsufficient(t, "Identity.Whoami", fe, names)
			}
		})
	}
}

// GitLab answers a refusal's message as a list of strings or an object of field
// errors on some routes, and that text reaches the caller as the string form does.
func TestAGitLabRefusalWhoseMessageIsNotAStringCarriesItsText(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   string
		status int
		names  string
	}{
		{name: "a_list", status: http.StatusConflict, body: `{"message":["Another open merge request already exists for this source branch: !1"]}`, names: "Another open merge request"},
		{name: "an_object", status: http.StatusBadRequest, body: `{"message":{"title":["can't be blank"]}}`, names: "can't be blank"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fe := forgeErrorOf(t, "Identity.Whoami", refusedOn(t, spec.GitLab, "Identity.Whoami", test.status, test.body))
			if !strings.Contains(fe.Message, test.names) {
				t.Errorf("Whoami on GitLab answered %d %s = message %q, want it to carry %q", test.status, test.body, fe.Message, test.names)
			}
		})
	}
}

// GitHub's fine-grained refusal carries message text only, so that family answers
// it as the plain forbidden it reads.
func TestAGitHubFineGrainedRefusalIsAPlainForbidden(t *testing.T) {
	const body = `{"message":"Resource not accessible by personal access token","documentation_url":"https://docs.github.com/rest","status":"403"}`
	err := refusedOn(t, spec.GitHub, "Identity.Whoami", http.StatusForbidden, body)
	fe := forgeErrorOf(t, "Identity.Whoami", err)
	if fe.Kind != forgeapi.KindForbidden || fe.Code != "" {
		t.Errorf("Whoami on GitHub answered 403 %s = (%v, %q), want (%v, no code)", body, fe.Kind, fe.Code, forgeapi.KindForbidden)
	}
}

// grantServer answers the GitLab grant's own read with that case's fixture and
// refuses one REST route for a permission the credential lacks.
func grantServer(t *testing.T, refusedPath, permission string) (*httptest.Server, *recorder) {
	t.Helper()
	f, err := loadFixture(spec.GitLab, "Capabilities.GrantCaps")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	document := f.Routes[0]
	if !document.GraphQL {
		t.Fatalf("Setup: %s serves %s %s first, want the document the grant reads", f.path, document.Method, document.Path)
	}
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, maxFixtureBody))
		if readErr != nil {
			raw = nil
		}
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), body: raw})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			for k, v := range document.Headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(document.Status)
			if _, writeErr := w.Write(document.Body); writeErr != nil {
				t.Errorf("Setup: writing the document answer: %v", writeErr)
			}
		case r.Method == http.MethodGet && r.URL.Path == refusedPath:
			w.WriteHeader(http.StatusForbidden)
			if _, writeErr := io.WriteString(w, fineGrainedRefusal(permission)); writeErr != nil {
				t.Errorf("Setup: writing the refusal: %v", writeErr)
			}
		default:
			rec.miss(r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// grantOf reads the grant capabilities, failing the case where the accessor fails.
func grantOf(t *testing.T, client any) forgeapi.GrantCaps {
	t.Helper()
	caps, ok := client.(forgeapi.Capabilities)
	if !ok {
		t.Fatal(roleAbsent("Capabilities"))
	}
	got, err := guard(func() (any, error) { return caps.GrantCaps(t.Context()) })
	if err != nil {
		t.Fatalf("GrantCaps on GitLab = error %v, want the grant", err)
	}
	return got.(forgeapi.GrantCaps)
}

// Once a request on the connection is refused for a permission the credential
// lacks, the merge-state grant is unknown with that refusal as its evidence: GitLab
// checks the token's own permission before the owner's access, and the permission
// pair a document reports is only the owner's, so the refusal outranks the pair.
func TestAGitLabScopeRefusalMakesTheMergeStateGrantUnknownWithItsEvidence(t *testing.T) {
	refusals := []struct {
		method     string
		path       string
		permission string
	}{
		{method: "PullRequests.ListMyPRs", path: "/api/v4/merge_requests", permission: mergeRequestRead},
		{method: "Identity.Whoami", path: "/api/v4/user", permission: "[User: Read]"},
	}
	for _, refusal := range refusals {
		t.Run(refusal.method, func(t *testing.T) {
			e := requireEntry(t, spec.GitLab, "Capabilities.GrantCaps")
			grant, ok := operationFor(e.Method)
			if !ok {
				t.Fatalf("Setup: the contract declares no case for %s", e.Method)
			}
			srv, rec := grantServer(t, refusal.path, refusal.permission)
			conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(spec.GitLab)}
			client, err := guard(func() (any, error) { return newClient(spec.GitLab, conn, offlineOptions(srv)...) })
			if err != nil {
				t.Fatalf("Setup: GitLab client: %v", err)
			}
			s := canonicalSubject(spec.GitLab)
			if prelude := preludeMethod(e, grant); prelude != "" {
				runPrelude(t, e, prelude, client, s)
			}
			runPrelude(t, e, "PullRequests.ReadPR", client, s)
			before := grantOf(t, client)
			if got := before.Caps[forgeapi.CapReadMergeState]; got != forgeapi.SupportYes || before.Ev[forgeapi.CapReadMergeState].Source == forgeapi.EvidenceDefault {
				t.Fatalf("Setup: GrantCaps after the read document = (%v, evidence %+v), want %v from the permission pair the document reported",
					got, before.Ev[forgeapi.CapReadMergeState], forgeapi.SupportYes)
			}

			refused, ok := operationFor(refusal.method)
			if !ok {
				t.Fatalf("Setup: the contract declares no case for %s", refusal.method)
			}
			_, refusedErr := guard(func() (any, error) { return refused.invoke(t.Context(), client, s) })
			if fe := forgeErrorOf(t, refusal.method, refusedErr); fe.Code != "scope_insufficient" {
				t.Errorf("%s on GitLab = code %q, want %q", refusal.method, fe.Code, "scope_insufficient")
			}
			sentBefore := rec.count()
			after := grantOf(t, client)

			if n := rec.count() - sentBefore; n != 0 {
				t.Errorf("GrantCaps after the refusal sent %d request(s), want 0: the accessor reads what the connection holds", n)
			}
			if got := after.Caps[forgeapi.CapReadMergeState]; got != forgeapi.SupportUnknown {
				t.Errorf("GrantCaps after %s was refused for %s = %v, want %v", refusal.method, refusal.permission, got, forgeapi.SupportUnknown)
			}
			ev := after.Ev[forgeapi.CapReadMergeState]
			_, name, _ := strings.Cut(refusal.method, ".")
			if ev.Source != forgeapi.EvidenceResponseBody || !strings.Contains(ev.Detail, name) || !strings.Contains(ev.Detail, refusal.permission) {
				t.Errorf("GrantCaps after the refusal = evidence {%q, %q}, want source %q with a detail naming %s and %s",
					ev.Source, ev.Detail, forgeapi.EvidenceResponseBody, name, refusal.permission)
			}
			if misses := rec.misses(); len(misses) > 0 {
				t.Errorf("the connection sent request(s) this case's server does not answer: %v", misses)
			}
		})
	}
}

// A refusal's text is the instance's, and an instance can echo the credential the
// request carried in any member of it, so on every product the refusal the caller
// receives quotes the instance's words and carries none of the token.
func TestARefusalEchoingTheCredentialCarriesNoneOfIt(t *testing.T) {
	const token = "conformance-placeholder"
	body := `{"message":"echoed credential: ` + token + `","error":"insufficient_scope","error_description":"` + token + ` lacks read_user"}`
	for _, p := range spec.Products {
		t.Run(string(p), func(t *testing.T) {
			fe := forgeErrorOf(t, "Identity.Whoami", refusedOn(t, p, "Identity.Whoami", http.StatusForbidden, body))
			for _, text := range []string{fe.Message, fe.Error()} {
				if strings.Contains(text, token) {
					t.Errorf("Whoami on %s refused with %q, want none of the token the request carried", p, text)
				}
			}
			if !strings.Contains(fe.Message, "REDACTED") {
				t.Errorf("Whoami on %s refused with message %q, want the instance's words with the token redacted out of them", p, fe.Message)
			}
		})
	}
}
