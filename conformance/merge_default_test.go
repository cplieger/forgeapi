package conformance

import (
	"encoding/json"
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

// mergeAtTheDefault sends a merge at IntentDefault naming no strategy to p's merge
// fixture, served behind record where record is non-nil, and answers the requests
// that arrived and what the merge returned.
func mergeAtTheDefault(t *testing.T, p spec.Product, record *wire) ([]sent, error) {
	t.Helper()
	f, err := loadFixture(p, "Merges.MergePR")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if record != nil {
		f.Routes = append([]wire{*record}, f.Routes...)
	}
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
// configured one as default_merge_style. GitHub configures no default method, only
// allowed ones, so the one method a repository allows is sent.
func TestTheDefaultIntentMergesAsTheRepositoryIsConfigured(t *testing.T) {
	for _, tc := range []struct {
		set     map[string]any
		product spec.Product
		name    string
		field   string
		want    string
	}{
		{
			name: "GitHub_allowing_one_method", product: spec.GitHub, field: "merge_method", want: `"rebase"`,
			set: map[string]any{"allow_merge_commit": false, "allow_squash_merge": false, "allow_rebase_merge": true},
		},
		{name: "Gitea", product: spec.Gitea, field: "Do", want: `"rebase-merge"`, set: map[string]any{"default_merge_style": "rebase-merge"}},
		{name: "Forgejo", product: spec.Forgejo, field: "Do", want: `"squash"`, set: map[string]any{"default_merge_style": "squash"}},
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
			wantSent := 1
			if record != nil {
				wantSent = 2
			}
			if len(arrived) != wantSent {
				t.Fatalf("MergePR at the default intent on %s sent %v, want %d request(s): the record read where the product needs it, then the merge", tc.product, arrived, wantSent)
			}
			merge := arrived[len(arrived)-1]
			if got, _ := bodyField(merge.body, tc.field); string(got) != tc.want {
				t.Errorf("MergePR at the default intent on %s sent %s = %s in %s, want %s", tc.product, tc.field, got, merge, tc.want)
			}
		})
	}
}

// A repository whose record names no one configured merge is refused before any
// write, naming what it allows, because a merge sent with a guessed strategy lands
// history the repository's owner did not choose.
func TestTheDefaultIntentIsRefusedWhereTheRepositoryNamesNoOneMerge(t *testing.T) {
	for _, tc := range []struct {
		set     map[string]any
		product spec.Product
		name    string
		names   []string
	}{
		{
			name: "GitHub_allowing_several_methods", product: spec.GitHub, names: []string{"merge", "squash"},
			set: map[string]any{"allow_merge_commit": true, "allow_squash_merge": true, "allow_rebase_merge": false},
		},
		{
			name: "GitHub_record_carrying_no_flags", product: spec.GitHub,
			set: map[string]any{"allow_merge_commit": nil, "allow_squash_merge": nil, "allow_rebase_merge": nil},
		},
		{name: "Gitea_record_carrying_no_default", product: spec.Gitea, names: []string{"merge", "squash"}, set: map[string]any{"default_merge_style": nil}},
		{name: "Forgejo_record_carrying_no_default", product: spec.Forgejo, names: []string{"merge", "squash"}, set: map[string]any{"default_merge_style": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := repositoryRecord(t, tc.product, tc.set)
			arrived, err := mergeAtTheDefault(t, tc.product, &record)
			var fe *forgeapi.Error
			if !asForgeError(err, &fe) {
				t.Fatalf("MergePR at the default intent on %s = %v, want a *forgeapi.Error", tc.product, err)
			}
			if fe.Code != forgeapi.CodeStrategyRequired || fe.Kind != forgeapi.KindNotMergeable {
				t.Errorf("MergePR at the default intent on %s = (%q, %v), want (%q, %v)", tc.product, fe.Code, fe.Kind, forgeapi.CodeStrategyRequired, forgeapi.KindNotMergeable)
			}
			if len(arrived) != 1 || arrived[0].method != "GET" {
				t.Errorf("MergePR at the default intent on %s sent %v, want the record read alone and no write", tc.product, arrived)
			}
			for _, name := range tc.names {
				if !strings.Contains(fe.Message, name) {
					t.Errorf("MergePR at the default intent on %s = message %q, want it naming the allowed %q", tc.product, fe.Message, name)
				}
			}
			if strings.Contains(fe.Message, "rebase") {
				t.Errorf("MergePR at the default intent on %s = message %q, naming rebase, which the record does not allow", tc.product, fe.Message)
			}
		})
	}
}
