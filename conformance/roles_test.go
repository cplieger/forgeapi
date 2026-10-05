package conformance

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// roleTypes are the published roles, keyed by the name the table's method spelling
// opens with.
var roleTypes = map[string]reflect.Type{
	"Identity":     reflect.TypeFor[forgeapi.Identity](),
	"Repos":        reflect.TypeFor[forgeapi.Repos](),
	"PullRequests": reflect.TypeFor[forgeapi.PullRequests](),
	"Merges":       reflect.TypeFor[forgeapi.Merges](),
	"Checks":       reflect.TypeFor[forgeapi.Checks](),
	"Issues":       reflect.TypeFor[forgeapi.Issues](),
	"Capabilities": reflect.TypeFor[forgeapi.Capabilities](),
	"Governor":     reflect.TypeFor[forgeapi.Governor](),
	"Releases":     reflect.TypeFor[forgeapi.Releases](),
	"Labels":       reflect.TypeFor[forgeapi.Labels](),
}

// TestTheRolesDeclareEveryOperationTheContractDrives joins the contract's method
// spelling to the roles: every operation the suite drives is a method of the role its
// spelling names, so a consumer holding Core reaches it without a type assertion on
// a family's client.
func TestTheRolesDeclareEveryOperationTheContractDrives(t *testing.T) {
	for _, op := range operations {
		role, name, _ := strings.Cut(op.method, ".")
		rt, ok := roleTypes[role]
		if !ok {
			t.Errorf("the contract drives %s, whose role %q is not a published role", op.method, role)
			continue
		}
		if _, ok := rt.MethodByName(name); !ok {
			t.Errorf("forgeapi.%s declares no %s, want it: the contract drives %s as that role's method", role, name, op.method)
		}
	}
}

// TestTheCrossRepositoryIssueListTakesTheListOptionsAlone holds the issue list's
// signature to the pull-request list it pairs with: no repository, the list-option
// vocabulary, and a page of issues.
func TestTheCrossRepositoryIssueListTakesTheListOptionsAlone(t *testing.T) {
	m, ok := reflect.TypeFor[forgeapi.Issues]().MethodByName("ListMyIssues")
	if !ok {
		t.Fatal("forgeapi.Issues declares no ListMyIssues, want the cross-repository issue list")
	}
	want := reflect.TypeFor[crossRepositoryIssues]().Method(0).Type
	if m.Type != want {
		t.Errorf("forgeapi.Issues.ListMyIssues is %v, want %v", m.Type, want)
	}
}

// TestTheRunListingAnswersTheRunRecord holds the run listing's signature and its row:
// one repository and the list-option vocabulary in, a page of runs out, and a run
// carrying its repository, its display name, its branch, its head, its page, its two
// times and its folded verdict. A run carries no id, no number and no trigger
// event: an id is a product token, and the event is one product's vocabulary.
func TestTheRunListingAnswersTheRunRecord(t *testing.T) {
	m, ok := listRunsMethod()
	if !ok {
		t.Fatal("forgeapi.Checks declares no ListRuns, want one repository's run listing")
	}
	in := []reflect.Type{
		reflect.TypeFor[context.Context](),
		reflect.TypeFor[forgeapi.RepoRef](),
		reflect.TypeFor[[]forgeapi.ListOption](),
	}
	if m.Type.NumIn() != len(in) || !m.Type.IsVariadic() {
		t.Fatalf("forgeapi.Checks.ListRuns is %v, want func(context.Context, forgeapi.RepoRef, ...forgeapi.ListOption) (forgeapi.Page[forgeapi.Run], error)", m.Type)
	}
	for i, want := range in {
		if got := m.Type.In(i); got != want {
			t.Errorf("forgeapi.Checks.ListRuns parameter %d is %v, want %v", i, got, want)
		}
	}
	if m.Type.NumOut() != 2 || m.Type.Out(1) != reflect.TypeFor[error]() {
		t.Fatalf("forgeapi.Checks.ListRuns answers %v, want a page of runs and an error", m.Type)
	}
	page := m.Type.Out(0)
	if page.PkgPath() != reflect.TypeFor[forgeapi.Page[forgeapi.Issue]]().PkgPath() || !strings.HasPrefix(page.Name(), "Page[") {
		t.Fatalf("forgeapi.Checks.ListRuns answers %v, want a forgeapi.Page", page)
	}
	items, ok := page.FieldByName("Items")
	if !ok || items.Type.Kind() != reflect.Slice {
		t.Fatalf("%v carries no Items slice", page)
	}
	run := items.Type.Elem()
	if run.Name() != "Run" || run.PkgPath() != page.PkgPath() || run.Kind() != reflect.Struct {
		t.Fatalf("forgeapi.Checks.ListRuns answers a page of %v, want forgeapi.Run", run)
	}
	fields := map[string]reflect.Type{
		"Repo":      reflect.TypeFor[forgeapi.RepoRef](),
		"Name":      reflect.TypeFor[string](),
		"Branch":    reflect.TypeFor[string](),
		"HeadSHA":   reflect.TypeFor[string](),
		"WebURL":    reflect.TypeFor[string](),
		"CreatedAt": reflect.TypeFor[time.Time](),
		"UpdatedAt": reflect.TypeFor[time.Time](),
		"State":     reflect.TypeFor[forgeapi.CheckState](),
	}
	for name, want := range fields {
		f, ok := run.FieldByName(name)
		if !ok {
			t.Errorf("forgeapi.Run carries no %s, want a %v", name, want)
			continue
		}
		if f.Type != want {
			t.Errorf("forgeapi.Run.%s is %v, want %v", name, f.Type, want)
		}
	}
	for _, name := range []string{"ID", "Number", "Event"} {
		if _, ok := run.FieldByName(name); ok {
			t.Errorf("forgeapi.Run carries %s, want none: it is a product token or one product's vocabulary", name)
		}
	}
}
