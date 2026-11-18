package conformance

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// repositoryRecord is p's recorded repository record with set written over its body,
// a nil value striking the key.
func repositoryRecord(t *testing.T, p spec.Product, set map[string]any) wire {
	t.Helper()
	f, err := loadFixture(p, "Capabilities.RepoAffordances")
	if err != nil || len(f.Routes) != 1 {
		t.Fatalf("Setup: %s's repository record: %d route(s), error %v, want one", p, len(f.Routes), err)
	}
	record := f.Routes[0]
	var body map[string]any
	if err := json.Unmarshal(record.Body, &body); err != nil {
		t.Fatalf("Setup: decoding %s's repository record: %v", p, err)
	}
	for k, v := range set {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	if record.Body, err = json.Marshal(body); err != nil {
		t.Fatalf("Setup: re-encoding %s's repository record: %v", p, err)
	}
	return record
}

// noQueue is GitHub's base branch rules naming no merge queue, as the sandbox's
// default branch answers them.
var noQueue = wire{Method: "GET", Path: "/repos/example/example/rules/branches/main", Status: 200, Body: json.RawMessage(`[]`)}

// mergeAtTheDefault sends a merge at IntentDefault naming no strategy to p's merge
// fixture, served behind record where record is non-nil and behind first, and answers
// the requests that arrived and what the merge returned.
func mergeAtTheDefault(t *testing.T, p spec.Product, record *wire, first ...wire) ([]sent, error) {
	t.Helper()
	f, err := loadFixture(p, "Merges.MergePR")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if record != nil {
		f.Routes = append([]wire{*record}, f.Routes...)
	}
	f.Routes = append(first, f.Routes...)
	srv, rec := newFixtureServer(t, f)
	conn := forgeapi.Connection{WebBaseURL: srv.URL, APIBaseURL: srv.URL + apiRoot(p)}
	client, err := guard(func() (any, error) { return newClient(p, conn, offlineOptions(srv)...) })
	if err != nil {
		t.Fatalf("Setup: %s client: %v", p, err)
	}
	s := canonicalSubject(p)
	_, err = guard(func() (any, error) {
		return client.(forgeapi.Merges).MergePR(t.Context(), s.repo, s.pr, forgeapi.MergeRequest{Intent: forgeapi.IntentDefault, HeadSHA: s.ref})
	})
	if misses := rec.misses(); len(misses) > 0 {
		t.Errorf("a merge at the default intent on %s reached %v, which no fixture answers", p, misses)
	}
	return rec.since(0), err
}

// A merge at the default intent merges as the repository is configured to. GitLab's
// merge route applies the project's merge method itself and updates the merge
// request's squash flag only on a true squash (lib/api/merge_requests.rb,
// build_merge_params), so the merge alone is sent. The Gitea family's route requires a
// style (MergePullRequestOption's Do), and the repository record carries the
// configured one as default_merge_style. GitHub configures no repository-wide default
// method, only allowed ones, so the one method a repository allows is sent, else the
// method of the base branch's merge queue, which merges by it whatever is sent.
func TestTheDefaultIntentMergesAsTheRepositoryIsConfigured(t *testing.T) {
	for _, tc := range []struct {
		set     map[string]any
		product spec.Product
		name    string
		field   string
		want    string
		reads   int
	}{
		{
			name: "GitHub_allowing_one_method", product: spec.GitHub, field: "merge_method", want: `"rebase"`, reads: 1,
			set: map[string]any{"allow_merge_commit": false, "allow_squash_merge": false, "allow_rebase_merge": true},
		},
		{
			name: "GitHub_allowing_several_behind_a_merge_queue", product: spec.GitHub, field: "merge_method", want: `"squash"`, reads: 3,
			set: map[string]any{"allow_merge_commit": true, "allow_squash_merge": true, "allow_rebase_merge": true},
		},
		{name: "Gitea", product: spec.Gitea, field: "Do", want: `"rebase-merge"`, reads: 1, set: map[string]any{"default_merge_style": "rebase-merge"}},
		{name: "Forgejo", product: spec.Forgejo, field: "Do", want: `"squash"`, reads: 1, set: map[string]any{"default_merge_style": "squash"}},
		{name: "GitLab", product: spec.GitLab, field: "squash", want: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var record *wire
			if tc.set != nil {
				w := repositoryRecord(t, tc.product, tc.set)
				record = &w
			}
			arrived, err := mergeAtTheDefault(t, tc.product, record)
			if err != nil {
				t.Fatalf("MergePR at the default intent on %s = %v, want the merge sent", tc.product, err)
			}
			if len(arrived) != tc.reads+1 {
				t.Fatalf("MergePR at the default intent on %s sent %v, want %d request(s): the %d read(s) the product needs, then the merge", tc.product, arrived, tc.reads+1, tc.reads)
			}
			merge := arrived[len(arrived)-1]
			if got, _ := bodyField(merge.body, tc.field); string(got) != tc.want {
				t.Errorf("MergePR at the default intent on %s sent %s = %s in %s, want %s", tc.product, tc.field, got, merge, tc.want)
			}
		})
	}
}

// A repository naming no one configured merge is refused before any write, naming what
// it allows, because a merge sent with a guessed strategy lands
// history the repository's owner did not choose.
// noStyleAllowed is a Gitea-family record naming no default style and allowing none.
var noStyleAllowed = map[string]any{
	"default_merge_style": nil, "allow_merge_commits": false, "allow_squash_merge": false,
	"allow_rebase": false, "allow_rebase_explicit": false, "allow_fast_forward_only_merge": false,
}

func TestTheDefaultIntentIsRefusedWhereTheRepositoryNamesNoOneMerge(t *testing.T) {
	for _, tc := range []struct {
		set     map[string]any
		product spec.Product
		name    string
		names   []string
		reads   int
	}{
		{
			name: "GitHub_allowing_several_methods_with_no_merge_queue", product: spec.GitHub, names: []string{"merge", "squash"}, reads: 3,
			set: map[string]any{"allow_merge_commit": true, "allow_squash_merge": true, "allow_rebase_merge": false},
		},
		{
			name: "GitHub_record_carrying_no_flags", product: spec.GitHub, reads: 3,
			set: map[string]any{"allow_merge_commit": nil, "allow_squash_merge": nil, "allow_rebase_merge": nil},
		},
		{name: "Gitea_record_carrying_no_default", product: spec.Gitea, names: []string{"merge", "squash"}, set: map[string]any{"default_merge_style": nil}},
		{name: "Forgejo_record_carrying_no_default", product: spec.Forgejo, names: []string{"merge", "squash"}, set: map[string]any{"default_merge_style": nil}},
		{name: "Gitea_record_allowing_no_style", product: spec.Gitea, set: noStyleAllowed},
		{name: "Forgejo_record_allowing_no_style", product: spec.Forgejo, set: noStyleAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := repositoryRecord(t, tc.product, tc.set)
			arrived, err := mergeAtTheDefault(t, tc.product, &record, noQueue)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("MergePR at the default intent on %s = %v, want a *forgeapi.Error", tc.product, err)
			}
			if fe.Code != forgeapi.CodeStrategyRequired || fe.Kind != forgeapi.KindNotMergeable {
				t.Errorf("MergePR at the default intent on %s = (%q, %v), want (%q, %v)", tc.product, fe.Code, fe.Kind, forgeapi.CodeStrategyRequired, forgeapi.KindNotMergeable)
			}
			reads := max(tc.reads, 1)
			if len(arrived) != reads || slices.ContainsFunc(arrived, func(s sent) bool { return s.method != "GET" }) {
				t.Errorf("MergePR at the default intent on %s sent %v, want %d read(s) and no write", tc.product, arrived, reads)
			}
			for _, name := range tc.names {
				if !strings.Contains(fe.Message, name) {
					t.Errorf("MergePR at the default intent on %s = message %q, want it naming the allowed %q", tc.product, fe.Message, name)
				}
			}
			if strings.Contains(fe.Message, "rebase") {
				t.Errorf("MergePR at the default intent on %s = message %q, naming rebase, which the record does not allow", tc.product, fe.Message)
			}
			if strings.Contains(fe.Message, "allows ,") || strings.Contains(fe.Message, " , ") {
				t.Errorf("MergePR at the default intent on %s = message %q, listing an empty allowed set", tc.product, fe.Message)
			}
		})
	}
}
