package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// giteaArrayLists are the Gitea family's page-numbered lists whose route answers an
// array of rows. The run listing, whose rows ride an object beside their total, is
// held to the same rule in runs_test.go.
var giteaArrayLists = []string{
	"Repos.ListRepos",
	"PullRequests.ListPRs",
	"PullRequests.ListMyPRs",
	"Issues.ListIssues",
	"Issues.ListMyIssues",
	"Releases.ListReleases",
	"Labels.ListLabels",
}

// An instance stating a maximum below the page bound serves pages of that maximum
// whatever limit is asked for, so a list asking for the bound reads its first page
// as short, and so as the end. Asking for the maximum makes every page but the last
// a full one and the short page the end again, so the walk reaches every row.
func TestAGiteaFamilyListAsksForNoMoreRowsThanItsInstanceStates(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		for _, method := range giteaArrayLists {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				pi := newPagedInstance(t, p, 7, 5)
				client := pagedClient(t, p, pi.serve(t))

				got := walkPaged(t, e, client, pi, 4)
				if !slices.Equal(got.sizes, []int{5, 2}) {
					t.Errorf("%s on %s over 7 rows on an instance stating 5 answered pages of %v, want [5 2]: every row, the short page last", method, p, got.sizes)
				}
				if want := [][2]string{{"1", "5"}, {"2", "5"}}; !slices.Equal(got.queries, want) {
					t.Errorf("%s on %s sent (page, limit) %v, want %v: the smaller of the page bound and the stated maximum", method, p, got.queries, want)
				}
			})
		}
	}
}

// The stated maximum is a ceiling on the limit, not a page size of its own, so a
// bound below it is the limit sent.
func TestAGiteaFamilyListAsksForItsPageBoundBelowTheInstancesMaximum(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		for _, method := range giteaArrayLists {
			t.Run(string(p)+"_"+method, func(t *testing.T) {
				e := requireEntry(t, p, method)
				pi := newPagedInstance(t, p, 7, 50)
				client := pagedClient(t, p, pi.serve(t))

				got := walkPaged(t, e, client, pi, 4, forgeapi.WithPageBound(3))
				if !slices.Equal(got.sizes, []int{3, 3, 1}) {
					t.Errorf("%s(WithPageBound(3)) on %s over 7 rows answered pages of %v, want [3 3 1]", method, p, got.sizes)
				}
				if want := [][2]string{{"1", "3"}, {"2", "3"}, {"3", "3"}}; !slices.Equal(got.queries, want) {
					t.Errorf("%s(WithPageBound(3)) on %s sent (page, limit) %v, want %v", method, p, got.queries, want)
				}
			})
		}
	}
}

// A connection built with the family's constructor and never set up holds no
// maximum, so its first page-numbered list reads it first, and the connection holds
// it for every list after.
func TestAGiteaFamilyConnectionHoldingNoMaximumReadsItOnceBeforeItsFirstList(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			pi := newPagedInstance(t, p, 3, 50)
			client := pagedClient(t, p, pi.serve(t))
			s := canonicalSubject(p)
			for _, method := range []string{"Releases.ListReleases", "Labels.ListLabels"} {
				if _, _, err := callList(t, requireEntry(t, p, method), client, pi.rec, s); err != nil {
					t.Fatalf("%s on %s = error %v, want the page", method, p, err)
				}
			}

			window := pi.rec.since(0)
			if n := settingsReads(window); n != 1 {
				t.Errorf("two lists on one %s connection read %s %d time(s) [%s], want once: the connection holds the maximum", p, settingsRoute, n, describeRequests(window))
			}
			if len(window) == 0 || window[0].path != settingsRoute {
				t.Errorf("two lists on one %s connection sent [%s], want %s first: no page is asked for before the maximum is held", p, describeRequests(window), settingsRoute)
			}
		})
	}
}

// The maximum is one of the connection's discoveries, so the connection's own row
// pays for it at setup, within that row's published price, and a list after setup
// sends its page alone, at the maximum the setup read.
func TestTheGiteaFamilySetupReadsTheStatedMaximumBesideTheVersion(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			e := requireEntry(t, p, "Capabilities.ConnectionCaps")
			setup, err := loadFixture(p, "Capabilities.ConnectionCaps")
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			pi := newPagedInstance(t, p, 3, 5)
			pi.extras = append(pi.extras, setup.Routes...)
			client := pagedClient(t, p, pi.serve(t))

			if err := connectionCaps(t, client); err != nil {
				t.Fatalf("ConnectionCaps on %s = error %v, want the connection's capabilities", p, err)
			}
			window := pi.rec.since(0)
			if n := settingsReads(window); n != 1 {
				t.Errorf("ConnectionCaps on %s read %s %d time(s) [%s], want once, beside the version", p, settingsRoute, n, describeRequests(window))
			}
			if low, high := price(&e, 1); len(window) < low || len(window) > high {
				t.Errorf("ConnectionCaps on %s sent %d request(s) [%s], want %d to %d: the connection's row prices the setup read", p, len(window), describeRequests(window), low, high)
			}

			_, after, err := callList(t, requireEntry(t, p, "Releases.ListReleases"), client, pi.rec, canonicalSubject(p))
			if err != nil {
				t.Fatalf("ListReleases on %s after setup = error %v, want the page", p, err)
			}
			if len(after) != 1 || !pi.isList(after[0]) {
				t.Fatalf("ListReleases on %s after setup sent [%s], want its one page and no further read of the maximum", p, describeRequests(after))
			}
			if limit := pageLimit(after[0]); limit != "5" {
				t.Errorf("ListReleases on %s after setup sent limit %s, want 5, the maximum the setup read", p, limit)
			}
		})
	}
}

// connectionCaps runs one client's connection accessor.
func connectionCaps(t *testing.T, client any) error {
	t.Helper()
	caps, ok := client.(forgeapi.Capabilities)
	if !ok {
		return roleAbsent("Capabilities")
	}
	_, err := guard(func() (any, error) {
		c, err := caps.ConnectionCaps(t.Context())
		return c, err
	})
	return err
}

// unreadableMaximums are settings answers from which no maximum can be held: a
// refusal, and answers carrying no positive max_response_items.
var unreadableMaximums = map[string]struct {
	body   string
	status int
}{
	"refused":      {status: http.StatusForbidden, body: `{"message":"Forbidden"}`},
	"no_maximum":   {status: http.StatusOK, body: `{"default_paging_num":30}`},
	"zero":         {status: http.StatusOK, body: settingsAnswer(0)},
	"negative":     {status: http.StatusOK, body: settingsAnswer(-1)},
	"not_a_number": {status: http.StatusOK, body: `{"max_response_items":"fifty"}`},
}

// A list that cannot hold the maximum cannot read a short page as the end, so it
// sends no page at all, and its refusal is the settings read's own.
func TestAGiteaFamilyListWhoseStatedMaximumCannotBeReadSendsNoPage(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		for name, answer := range unreadableMaximums {
			t.Run(string(p)+"_"+name, func(t *testing.T) {
				pi := newPagedInstance(t, p, 3, 50)
				pi.settingsStatus, pi.settingsBody = answer.status, answer.body
				client := pagedClient(t, p, pi.serve(t))

				got, window, err := callList(t, requireEntry(t, p, "Releases.ListReleases"), client, pi.rec, canonicalSubject(p))
				if lists := pi.listRequests(window); len(lists) != 0 {
					t.Errorf("ListReleases on %s with the maximum unreadable sent [%s], want no page", p, describeRequests(lists))
				}
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) {
					t.Fatalf("ListReleases on %s with the maximum unreadable = (%v, error %v), want a *forgeapi.Error", p, got, err)
				}
				if answer.status == http.StatusForbidden && (fe.Status != http.StatusForbidden || fe.Kind != forgeapi.KindForbidden) {
					t.Errorf("ListReleases on %s with the settings read refused = status %d kind %v, want status %d kind %v: the refusal is the read's own",
						p, fe.Status, fe.Kind, http.StatusForbidden, forgeapi.KindForbidden)
				}
			})
		}
	}
}

// Only a page-numbered list needs the maximum, so setup that cannot read it still
// succeeds, and the connection holds nothing, so its list reads it again.
func TestTheGiteaFamilySetupSucceedsWithoutTheStatedMaximumAndItsListAsksAgain(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			setup, err := loadFixture(p, "Capabilities.ConnectionCaps")
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			pi := newPagedInstance(t, p, 3, 50)
			pi.extras = append(pi.extras, setup.Routes...)
			pi.settingsStatus, pi.settingsBody = http.StatusForbidden, `{"message":"Forbidden"}`
			client := pagedClient(t, p, pi.serve(t))

			if err := connectionCaps(t, client); err != nil {
				t.Errorf("ConnectionCaps on %s with the settings read refused = error %v, want the capabilities: setup does not need the maximum", p, err)
			}
			_, _, err = callList(t, requireEntry(t, p, "Releases.ListReleases"), client, pi.rec, canonicalSubject(p))
			if err == nil {
				t.Errorf("ListReleases on %s with the settings read refused = nil error, want the refusal", p)
			}
			window := pi.rec.since(0)
			if n := settingsReads(window); n != 2 {
				t.Errorf("setup and one list on %s read %s %d time(s) [%s], want twice: a connection holding no maximum reads it before its list", p, settingsRoute, n, describeRequests(window))
			}
			if lists := pi.listRequests(window); len(lists) != 0 {
				t.Errorf("ListReleases on %s with the settings read refused sent [%s], want no page", p, describeRequests(lists))
			}
		})
	}
}

// The label lookup reads one page of labels and reads a short page as the whole
// collection, so on an instance stating a maximum below the products' shipped
// default it asks for that maximum: a full page with the name absent is then the
// truncated lookup it is, rather than a clamped page read as every label there is.
func TestAGiteaFamilyLabelLookupAsksForTheInstancesStatedMaximum(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			created, err := loadFixture(p, "Issues.CreateIssue")
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			pi := newPagedInstance(t, p, 7, 5)
			for _, w := range created.Routes {
				if w.Method == http.MethodPost {
					pi.extras = append(pi.extras, w)
				}
			}
			client := pagedClient(t, p, pi.serve(t), forgeapi.WithMutations(true))
			issues, ok := client.(forgeapi.Issues)
			if !ok {
				t.Fatalf("Setup: the %s client plays no Issues role", p)
			}
			s := canonicalSubject(p)

			_, err = guard(func() (any, error) {
				issue, err := issues.CreateIssue(t.Context(), s.repo, forgeapi.NewIssue{Title: "Example issue", Labels: []string{pagedLabel(7)}})
				return issue, err
			})
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeLabelPageTruncated {
				t.Errorf("CreateIssue(label %q) on %s over 7 labels on an instance stating 5 = error %v, want code %q: the page read was full",
					pagedLabel(7), p, err, forgeapi.CodeLabelPageTruncated)
			}
			window := pi.rec.since(0)
			for _, req := range pi.listRequests(window) {
				if limit := pageLimit(req); limit != "5" {
					t.Errorf("CreateIssue on %s read labels with limit %s in %s, want 5, the stated maximum", p, limit, req)
				}
			}
			for _, req := range window {
				if req.method == http.MethodPost {
					t.Errorf("CreateIssue on %s sent %s, want no creation: a label it names was not resolved", p, req)
				}
			}
		})
	}
}

// statusInstance serves one commit's combined status the way the Gitea family's
// instances serve it: pages of the smaller of the limit and the instance's maximum,
// and a state and count over the returned page, so only the rows tell the truth.
func statusInstance(t *testing.T, p spec.Product, contexts, serves int) (*httptest.Server, *recorder) {
	t.Helper()
	s := canonicalSubject(p)
	route := "/api/v1/repos/" + selector + "/commits/" + s.ref + "/status"
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(sent{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query()})
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case settingsRoute:
			if _, err := io.WriteString(w, settingsAnswer(serves)); err != nil {
				t.Errorf("Setup: writing the settings answer: %v", err)
			}
			return
		case route:
		default:
			rec.miss(r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		limit, page := serves, 1
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
			limit = min(v, serves)
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
			page = v
		}
		from, to := min((page-1)*limit, contexts), min(page*limit, contexts)
		statuses := make([]map[string]any, 0, to-from)
		for n := from + 1; n <= to; n++ {
			statuses = append(statuses, map[string]any{
				"context":     "ci/" + strconv.Itoa(n),
				"description": "Example check.",
				"target_url":  "https://ci.example/" + strconv.Itoa(n),
				"status":      "success",
			})
		}
		out, err := json.Marshal(map[string]any{
			"sha":         s.ref,
			"state":       "success",
			"total_count": len(statuses),
			"commit_url":  "https://forge.example/" + selector + "/commit/" + s.ref,
			"statuses":    statuses,
		})
		if err != nil {
			t.Errorf("Setup: encoding a status page: %v", err)
		}
		if _, err := w.Write(out); err != nil {
			t.Errorf("Setup: writing a status page: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// The folded status pages while a page comes back full, so on an instance stating a
// maximum below the products' shipped default it asks for that maximum, and a
// clamped page is followed rather than folded as every context there is.
func TestAGiteaFamilyFoldAsksForTheInstancesStatedMaximum(t *testing.T) {
	for _, p := range giteaFamilyProducts {
		t.Run(string(p), func(t *testing.T) {
			srv, rec := statusInstance(t, p, 7, 5)
			client := pagedClient(t, p, srv)
			checks, ok := client.(forgeapi.Checks)
			if !ok {
				t.Fatalf("Setup: the %s client plays no Checks role", p)
			}
			s := canonicalSubject(p)

			got, err := guard(func() (any, error) {
				c, err := checks.CommitStatus(t.Context(), s.repo, s.ref)
				return c, err
			})
			if err != nil {
				t.Fatalf("CommitStatus on %s = error %v, want the fold", p, err)
			}
			if folded := got.(forgeapi.CommitChecks); folded.Total != 7 || folded.Passing != 7 {
				t.Errorf("CommitStatus on %s over 7 contexts on an instance stating 5 = total %d passing %d, want 7 and 7: every context folded",
					p, folded.Total, folded.Passing)
			}
			var limits []string
			for _, req := range rec.since(0) {
				if strings.HasSuffix(req.path, "/status") {
					limits = append(limits, req.query.Get("limit"))
				}
			}
			if want := []string{"5", "5"}; !slices.Equal(limits, want) {
				t.Errorf("CommitStatus on %s asked for limits %v, want %v: the stated maximum on every page until a short one", p, limits, want)
			}
		})
	}
}
