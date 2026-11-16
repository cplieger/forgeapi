package conformance

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3/internal/spec"
)

// labelLaneFixture is one product's label change fixture with the routes the live
// setup and its read-back reach beside it: the issue the setup opens and closes, the
// label it puts on that issue, and the issue read back carrying labels.
func labelLaneFixture(t *testing.T, p spec.Product, method string, readBack json.RawMessage) fixture {
	t.Helper()
	f, err := loadFixture(p, method)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	for _, other := range []string{"Issues.CreateIssue", "Issues.CloseIssue", "Labels.AddLabels"} {
		g, err := loadFixture(p, other)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		for _, w := range g.Routes {
			if w.Method != http.MethodGet {
				f.Routes = append(f.Routes, w)
			}
		}
	}
	read, err := loadFixture(p, "Issues.ReadIssue")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(read.Routes[0].Body, &record); err != nil {
		t.Fatalf("Setup: %s's issue record is not a JSON object: %v", read.path, err)
	}
	record["labels"] = readBack
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Setup: encoding the read-back record: %v", err)
	}
	f.Routes = append(f.Routes, wire{Method: http.MethodGet, Path: read.Routes[0].Path, Status: http.StatusOK, Body: body, Repeat: 2})
	return f
}

// runLabelLane drives one label change through the live lane against an instance
// serving f and answers what arrived.
func runLabelLane(t *testing.T, p spec.Product, method string, f fixture) ([]sent, bool) {
	t.Helper()
	srv, rec := newFixtureServer(t, f)
	pointLiveLaneAt(t, p, srv.URL, selector)
	e := requireEntry(t, p, method)
	op, ok := operationFor(method)
	if !ok {
		t.Fatalf("Setup: the contract declares no case for %s", method)
	}
	failed := false
	t.Run("live", func(t *testing.T) {
		defer func() { failed = t.Failed() }()
		runLive(t, e, op)
	})
	if misses := rec.misses(); len(misses) > 0 {
		t.Errorf("%s on %s live reached route(s) the instance does not answer: %v", method, p, misses)
	}
	return rec.since(0), failed
}

// The live removal labels the issue it opened before the case removes the label, and
// reads the issue back afterwards, which is the only witness of a change that answers
// no item: an issue read back still carrying the label fails the case.
func TestALiveLabelRemovalLabelsItsIssueFirstAndReadsItBack(t *testing.T) {
	for _, p := range []spec.Product{spec.Gitea, spec.GitLab} {
		t.Run(string(p), func(t *testing.T) {
			f := labelLaneFixture(t, p, "Labels.RemoveLabels", json.RawMessage(`[]`))
			arrived, failed := runLabelLane(t, p, "Labels.RemoveLabels", f)
			if failed {
				t.Fatalf("RemoveLabels on %s live failed on an issue read back without the label: %s", p, describeRequests(arrived))
			}
			labelled, removed := -1, -1
			for i, req := range arrived {
				switch {
				case removed < 0 && labelled < 0 && strings.Contains(string(req.body), labelName):
					labelled = i
				case labelled >= 0 && (req.method == http.MethodDelete || strings.Contains(string(req.body), "remove_labels")):
					removed = i
				}
			}
			if labelled < 0 || removed < labelled {
				t.Errorf("RemoveLabels on %s live sent %s, want the setup's labelling before the case's removal", p, describeRequests(arrived))
			}
		})
	}
}

// The read-back is what fails a live label change whose effect did not land: an
// issue still carrying the label after a removal, or lacking it after an addition.
func TestTheLiveReadBackRefusesALabelChangeThatDidNotLand(t *testing.T) {
	for name, tc := range map[string]struct {
		labels string
		want   bool
	}{
		"still_carried_after_a_removal": {labels: `[{"name":"` + labelName + `"}]`, want: false},
		"missing_after_an_addition":     {labels: `[]`, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := labelLaneFixture(t, spec.Gitea, "Labels.RemoveLabels", json.RawMessage(tc.labels))
			srv, _ := newFixtureServer(t, f)
			l := newLane(spec.Gitea, srv.URL, "conformance-placeholder", selector)
			s := canonicalSubject(spec.Gitea)
			if err := carriesLabel(t.Context(), l, s, tc.want); err == nil {
				t.Errorf("carriesLabel over labels %s, want %v = nil, want the read-back to refuse it", tc.labels, tc.want)
			}
			if err := carriesLabel(t.Context(), l, s, !tc.want); err != nil {
				t.Errorf("carriesLabel over labels %s, want %v = %v, want nil", tc.labels, !tc.want, err)
			}
		})
	}
}

// The lane reads an issue's labels as GitLab answers them, bare names, and as the
// other products do, label objects.
func TestTheLaneReadsAnIssuesLabelsInEitherShape(t *testing.T) {
	for _, p := range []spec.Product{spec.GitHub, spec.GitLab, spec.Gitea, spec.Forgejo} {
		t.Run(string(p), func(t *testing.T) {
			labels := `[{"name":"a"},{"name":"b"}]`
			if p == spec.GitLab {
				labels = `["a","b"]`
			}
			read, err := loadFixture(p, "Issues.ReadIssue")
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}
			route := read.Routes[0]
			var record map[string]json.RawMessage
			if err := json.Unmarshal(route.Body, &record); err != nil {
				t.Fatalf("Setup: %v", err)
			}
			record["labels"] = json.RawMessage(labels)
			route.Body, _ = json.Marshal(record)
			srv, _ := newFixtureServer(t, fixture{path: read.path, Routes: []wire{route}})
			l := newLane(p, srv.URL, "conformance-placeholder", selector)
			l.api = srv.URL + apiRoot(p)
			got, err := l.issueLabels(t.Context(), 1)
			if err != nil || !slices.Equal(got, []string{"a", "b"}) {
				t.Errorf("issueLabels on %s = %v, %v, want [a b]", p, got, err)
			}
		})
	}
}
