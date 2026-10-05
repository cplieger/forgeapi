package gitea

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// TestMapperAnswersOneCellPerStatus holds this family's error mapping to one case
// per row of the table it implements.
//
// Two of those rows are this family's own reading of a status the other two
// families read differently, and they are the reason the mapping is per family
// rather than per status: the merge's 409 names no cause here, because the route
// answers it for a head that moved and for a merge that conflicts in human text
// alone, and 423 is an archived repository, a permanent refusal whose remedy is an
// operator unarchiving it, so it is never retried and never reported as a conflict
// that a rebase could clear. A 405 is the merge refusal the other two answer for
// theirs: this product answers it for a merge style the repository disallows and
// for every other refusal of its merge route alike, so the status names no cause.
func TestMapperAnswersOneCellPerStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		header http.Header
		code   string
		status int
		kind   forgeapi.ErrorKind
	}{
		{name: "401_is_unauthorized", status: http.StatusUnauthorized, kind: forgeapi.KindUnauthorized},
		{name: "403_is_forbidden", status: http.StatusForbidden, kind: forgeapi.KindForbidden},
		{
			name:   "403_with_an_exhausted_window_is_a_throttle",
			status: http.StatusForbidden,
			header: http.Header{"Ratelimit-Remaining": {"0"}},
			kind:   forgeapi.KindRateLimited,
		},
		{
			name:   "404_is_the_union_of_the_two_absences",
			status: http.StatusNotFound,
			kind:   forgeapi.KindNotFound,
			code:   forgeapi.CodeRepoOrPRNotVisible,
		},
		{
			name:   "405_is_a_refusal_whose_cause_the_status_does_not_name",
			status: http.StatusMethodNotAllowed,
			kind:   forgeapi.KindNotMergeable,
			code:   forgeapi.CodeNotMergeable,
		},
		{
			name:   "409_on_the_merge_names_no_cause",
			status: http.StatusConflict,
			kind:   forgeapi.KindConflict,
			code:   forgeapi.CodeNotMergeable,
		},
		{
			name:   "422_is_validation",
			status: http.StatusUnprocessableEntity,
			kind:   forgeapi.KindNotMergeable,
			code:   forgeapi.CodeValidation,
		},
		{
			name:   "423_is_an_archived_repository",
			status: http.StatusLocked,
			kind:   forgeapi.KindForbidden,
			code:   forgeapi.CodeRepoArchived,
		},
		{name: "429_is_a_throttle", status: http.StatusTooManyRequests, kind: forgeapi.KindRateLimited},
		{name: "500_is_upstream", status: http.StatusInternalServerError, kind: forgeapi.KindUpstream},
		{name: "502_is_upstream", status: http.StatusBadGateway, kind: forgeapi.KindUpstream},
		{name: "an_unmapped_4xx_is_upstream_and_never_guessed", status: http.StatusTeapot, kind: forgeapi.KindUpstream},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := test.header
			if header == nil {
				header = http.Header{}
			}
			kind, code := mapper("MergePR", test.status, header, "")
			if kind != test.kind {
				t.Errorf("mapper(MergePR, %d, %v) = kind %v, want %v", test.status, header, kind, test.kind)
			}
			if code != test.code {
				t.Errorf("mapper(MergePR, %d, %v) = code %q, want %q", test.status, header, code, test.code)
			}
		})
	}
}

// TestA409IsMappedPerOperation holds the 409 cells off the merge: a creation's 409 is
// the pull request that already exists, a request upstream rejected as invalid, and
// any other operation's 409 is a conflict naming no cause.
func TestA409IsMappedPerOperation(t *testing.T) {
	for _, test := range []struct {
		op   string
		code string
	}{
		{op: "CreatePR", code: forgeapi.CodeValidation},
		{op: "CreateIssue", code: ""},
		{op: "ListPRs", code: ""},
	} {
		kind, code := mapper(test.op, http.StatusConflict, http.Header{}, "")
		if kind != forgeapi.KindConflict || code != test.code {
			t.Errorf("mapper(%s, 409) = %v / %q, want %v / %q", test.op, kind, code, forgeapi.KindConflict, test.code)
		}
	}
}

// TestA400IsTheUnresolvedOwnerOnTheScopedListsAlone holds the one cell this family
// maps per operation: the issue-search route answers 400 for an owner it cannot
// resolve, so on the two lists that read it a 400 is that owner, a not-found answer
// under the owner code. Everywhere else a 400 has no cell and stays upstream, because
// reading it as an owner on a call that named none would hand a caller a remedy for
// a fault it does not have.
func TestA400IsTheUnresolvedOwnerOnTheScopedListsAlone(t *testing.T) {
	for _, test := range []struct {
		op   string
		code string
		kind forgeapi.ErrorKind
	}{
		{op: "ListMyPRs", kind: forgeapi.KindNotFound, code: forgeapi.CodeOwnerUnresolved},
		{op: "ListMyIssues", kind: forgeapi.KindNotFound, code: forgeapi.CodeOwnerUnresolved},
		{op: "ListRepos", kind: forgeapi.KindUpstream},
		{op: "ListRuns", kind: forgeapi.KindUpstream},
		{op: "MergePR", kind: forgeapi.KindUpstream},
	} {
		t.Run(test.op, func(t *testing.T) {
			kind, code := mapper(test.op, http.StatusBadRequest, http.Header{}, "")
			if kind != test.kind || code != test.code {
				t.Errorf("mapper(%s, 400) = kind %v code %q, want kind %v code %q", test.op, kind, code, test.kind, test.code)
			}
		})
	}
}

// TestRunStateFoldsEachProductsOwnStatusTotally holds the run-status table to both
// products' spellings: Gitea's status, read through its conclusion once the run has
// completed, and Forgejo's status alone, which carries the outcome. A failure must
// fold to failing on either spelling, because a red run read as anything else is the
// answer a consumer filtering for failed runs never sees. A value neither product
// writes is unknown, counted and named, rather than folded into a verdict.
func TestRunStateFoldsEachProductsOwnStatusTotally(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     string
		conclusion string
		want       forgeapi.CheckState
	}{
		{name: "gitea_completed_success", status: "completed", conclusion: "success", want: forgeapi.CheckPassing},
		{name: "gitea_completed_failure", status: "completed", conclusion: "failure", want: forgeapi.CheckFailing},
		{name: "gitea_completed_cancelled", status: "completed", conclusion: "cancelled", want: forgeapi.CheckNeutral},
		{name: "gitea_completed_skipped", status: "completed", conclusion: "skipped", want: forgeapi.CheckNeutral},
		{name: "gitea_queued", status: "queued", want: forgeapi.CheckPending},
		{name: "gitea_pending", status: "pending", want: forgeapi.CheckPending},
		{name: "gitea_requested", status: "requested", want: forgeapi.CheckPending},
		{name: "gitea_waiting", status: "waiting", want: forgeapi.CheckPending},
		{name: "gitea_in_progress", status: "in_progress", want: forgeapi.CheckPending},
		{name: "gitea_completed_with_no_conclusion", status: "completed", want: forgeapi.CheckUnknown},
		{name: "forgejo_success", status: "success", want: forgeapi.CheckPassing},
		{name: "forgejo_failure", status: "failure", want: forgeapi.CheckFailing},
		{name: "forgejo_cancelled", status: "cancelled", want: forgeapi.CheckNeutral},
		{name: "forgejo_skipped", status: "skipped", want: forgeapi.CheckNeutral},
		{name: "forgejo_waiting", status: "waiting", want: forgeapi.CheckPending},
		{name: "forgejo_running", status: "running", want: forgeapi.CheckPending},
		{name: "forgejo_blocked", status: "blocked", want: forgeapi.CheckPending},
		{name: "forgejo_unknown", status: "unknown", want: forgeapi.CheckUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if got := h.client.runState(&wireRun{Status: test.status, Conclusion: test.conclusion}); got != test.want {
				t.Errorf("runState(status %q, conclusion %q) = %v, want %v", test.status, test.conclusion, got, test.want)
			}
			if fired := h.spy.times("UnknownEnumValue"); fired != 0 {
				t.Errorf("runState(status %q, conclusion %q) fired UnknownEnumValue %d time(s), want 0: the value is one a product writes",
					test.status, test.conclusion, fired)
			}
		})
	}
	for _, test := range []struct {
		name       string
		status     string
		conclusion string
		named      string
	}{
		{name: "an_unwritten_status", status: "future_status", named: "future_status"},
		{name: "an_unwritten_conclusion", status: "completed", conclusion: "future_conclusion", named: "future_conclusion"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if got := h.client.runState(&wireRun{Status: test.status, Conclusion: test.conclusion}); got != forgeapi.CheckUnknown {
				t.Errorf("runState(status %q, conclusion %q) = %v, want %v", test.status, test.conclusion, got, forgeapi.CheckUnknown)
			}
			if fired := h.spy.times("UnknownEnumValue"); fired != 1 {
				t.Errorf("runState(status %q, conclusion %q) fired UnknownEnumValue %d time(s), want 1", test.status, test.conclusion, fired)
			}
			if !h.logged(test.named) {
				t.Errorf("runState(status %q, conclusion %q) logged no line naming %q, want one", test.status, test.conclusion, test.named)
			}
		})
	}
}

// TestAnArchivedRepositoryIsNeverRetryable holds the one cell whose whole reason for
// existing is that it must not be retried: an archived repository is a permanent
// refusal, and reporting it as transient would have a caller retry a state no rebase
// and no re-read reaches.
func TestAnArchivedRepositoryIsNeverRetryable(t *testing.T) {
	h := newRefusingHarness(t, http.StatusLocked)
	_, err := h.client.Whoami(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("Whoami against an archived repository = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeRepoArchived {
		t.Errorf("Whoami = code %q, want %q", fe.Code, forgeapi.CodeRepoArchived)
	}
	if fe.Retryable {
		t.Error("Whoami = retryable true, want false: an archived repository is a permanent refusal whose remedy is an operator unarchiving it")
	}
	if fe.Status != http.StatusLocked {
		t.Errorf("Whoami = status %d, want %d: the real status is carried always", fe.Status, http.StatusLocked)
	}
}

// declaredEnums is what the two documents this family's products publish declare for
// the fields its totality tables map, cut from those documents into testdata.
//
// A null array is a document declaring the field as a bare string with no
// machine-readable set, which is one of the two products here and is the reason the
// other document is the shared contract: an enumeration nobody declares cannot be
// iterated, and the spellings survive there only inside a prose description.
type declaredEnums struct {
	Provenance string          `json:"//"`
	Gitea      declaredProduct `json:"gitea"`
	Forgejo    declaredProduct `json:"forgejo"`
}

// declaredProduct is one document's declarations, and declaredEnum is one field's.
type declaredProduct struct {
	Version string       `json:"version"`
	Status  declaredEnum `json:"commit_status_state"`
	State   declaredEnum `json:"issue_and_pull_request_state"`
}

type declaredEnum struct {
	DeclaredBy string   `json:"declared_by"`
	Prose      string   `json:"prose"`
	Enum       []string `json:"enum"`
}

// readDeclaredEnums loads that fixture, which is the specification side of every
// totality assertion below: a second hand-written list inside this file would hold
// the tables against a copy of themselves, so an upstream member addition would move
// nothing and fail nothing.
func readDeclaredEnums(t *testing.T) declaredEnums {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "enumerations.json"))
	if err != nil {
		t.Fatalf("Setup: reading the declared enumerations: %v", err)
	}
	var out declaredEnums
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Setup: decoding the declared enumerations: %v", err)
	}
	if out.Provenance == "" {
		t.Fatal("Setup: the declared enumerations carry no provenance line, so nothing says which documents they were cut from or when those were read")
	}
	if len(out.Gitea.Status.Enum) == 0 {
		t.Fatal("Setup: the declared enumerations carry no commit-status set for the document that declares one, which is what every assertion here reads")
	}
	return out
}

// TestCheckStateMapsEveryValueTheProductDeclares holds the table against the
// document that declares the field, then holds the unknown arm to the three things
// that arm owes: the unknown member, the counter and a log line NAMING the value.
//
// The fold is where an unmapped value does the most damage, because reporting a
// failing commit status as passing renders a red pull request green. What the table
// must equal is the document's own set plus the empty string, which is this
// library's own member for a field that did not arrive rather than one upstream
// declares.
func TestCheckStateMapsEveryValueTheProductDeclares(t *testing.T) {
	declared := readDeclaredEnums(t)
	document := declared.Gitea.Status
	want := map[string]forgeapi.CheckState{
		"success": forgeapi.CheckPassing,
		"failure": forgeapi.CheckFailing,
		"error":   forgeapi.CheckFailing,
		"pending": forgeapi.CheckPending,
		"warning": forgeapi.CheckNeutral,
		"skipped": forgeapi.CheckNeutral,
		"":        forgeapi.CheckUnknown,
	}
	for _, value := range document.Enum {
		if _, ok := checkStates[value]; !ok {
			t.Errorf("checkStates carries no entry for %q, which %s declares: a value the document declares and the table does not folds to unknown on every response that sends it",
				value, document.DeclaredBy)
		}
		if _, ok := want[value]; !ok {
			t.Errorf("this case states no member for %q, which %s declares", value, document.DeclaredBy)
		}
	}
	if len(checkStates) != len(document.Enum)+1 {
		t.Errorf("checkStates holds %d value(s), want %d: the set %s declares plus the empty string, which is this library's own member for a field that did not arrive",
			len(checkStates), len(document.Enum)+1, document.DeclaredBy)
	}
	// The other product of this family declares no set at all for the same field:
	// its definition is a bare string whose six spellings survive only in a prose
	// description, so there is nothing to iterate there and the document above is
	// the contract both products are held to.
	if other := declared.Forgejo.Status; other.Enum != nil {
		t.Errorf("the other document declares %v for %s, want none: the assertion above reads one document because only one declares a set, and that has changed",
			other.Enum, other.DeclaredBy)
	}
	h := newHarness(t, nil)
	for value, member := range want {
		if got := h.client.checkState(value); got != member {
			t.Errorf("checkState(%q) = %v, want %v", value, got, member)
		}
	}
	if fired := h.spy.times("UnknownEnumValue"); fired != 0 {
		t.Errorf("the declared values fired UnknownEnumValue %d time(s), want 0", fired)
	}
	if got := h.client.checkState("future_state"); got != forgeapi.CheckUnknown {
		t.Errorf("checkState(%q) = %v, want %v: a value outside the table is unknown rather than folded into a verdict", "future_state", got, forgeapi.CheckUnknown)
	}
	if fired := h.spy.times("UnknownEnumValue"); fired != 1 {
		t.Errorf("checkState(%q) fired UnknownEnumValue %d time(s), want 1: an enumeration that grew upstream is visible as that or not at all", "future_state", fired)
	}
	if !h.logged("future_state") {
		t.Errorf("checkState(%q) logged no line naming the value, want one: the counter says an unknown arrived and the line says which", "future_state")
	}
}

// TestAnUnmappedValueIsLoggedSanitizedWithinABound holds the line naming an unmapped
// value to what a line carrying upstream text owes a consumer's handler: a JSON
// handler emits a bidi override and a C1 control raw, so neither may reach it, and
// an instance can send a member of any length, so the line carries a bounded prefix
// rather than the whole. The member stays recognisable.
func TestAnUnmappedValueIsLoggedSanitizedWithinABound(t *testing.T) {
	hostile := "teleported\u202e\u0085" + strings.Repeat("x", 4096)
	var logs bytes.Buffer
	h := newHarness(t, nil, forgeapi.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	if got := h.client.checkState(hostile); got != forgeapi.CheckUnknown {
		t.Errorf("checkState(an unmapped member) = %v, want %v", got, forgeapi.CheckUnknown)
	}
	var record string
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, `"msg":"forgeapi unmapped enumeration value"`) {
			record = line
			break
		}
	}
	if record == "" {
		t.Fatalf("checkState(an unmapped member) logged no unmapped-value record; the JSON log is %q", logs.String())
	}
	if !strings.Contains(record, "teleported") {
		t.Errorf("the unmapped-value record = %q, want the member's recognisable prefix %q", record, "teleported")
	}
	if strings.ContainsRune(record, '\u202e') || strings.ContainsRune(record, '\u0085') {
		t.Errorf("the unmapped-value record carries U+202E or U+0085 raw, which a JSON handler emits with its control meaning; record %q", record)
	}
	if len(record) >= len(hostile) {
		t.Errorf("the unmapped-value record is %d bytes for a member of %d, want a bounded prefix rather than the whole", len(record), len(hostile))
	}
}

// TestPullStateAndIssueStateMapTotally holds the other two tables to the same rule,
// and holds each to the members its own swagger document closes the field at: a
// spelling the document does not declare must not be admitted as known, or a release
// that ever sent it meaning something else would bypass the unknown-value signal.
func TestPullStateAndIssueStateMapTotally(t *testing.T) {
	declared := readDeclaredEnums(t)
	document := declared.Gitea.State
	for _, table := range []struct {
		name  string
		holds func(string) bool
		size  int
	}{
		{name: "prStates", holds: func(v string) bool { _, ok := prStates[v]; return ok }, size: len(prStates)},
		{name: "issueStates", holds: func(v string) bool { _, ok := issueStates[v]; return ok }, size: len(issueStates)},
	} {
		for _, value := range document.Enum {
			if !table.holds(value) {
				t.Errorf("%s carries no entry for %q, which %s declares", table.name, value, document.DeclaredBy)
			}
		}
		if table.size != len(document.Enum)+1 {
			t.Errorf("%s holds %d value(s), want %d: the set %s declares plus the empty string, and a spelling the document does not declare must not be admitted as known",
				table.name, table.size, len(document.Enum)+1, document.DeclaredBy)
		}
	}
	if other := declared.Forgejo.State; other.Enum != nil {
		t.Errorf("the other document declares %v for %s, want none", other.Enum, other.DeclaredBy)
	}
	for _, test := range []struct {
		name   string
		state  string
		merged bool
		want   forgeapi.PRState
	}{
		{name: "open", state: "open", want: forgeapi.PRStateOpen},
		{name: "closed", state: "closed", want: forgeapi.PRStateClosed},
		{name: "merged_answers_the_closed_spelling_with_the_flag", state: "closed", merged: true, want: forgeapi.PRStateMerged},
		{name: "absent", state: "", want: forgeapi.PRStateUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			got := h.client.pullState(&wirePull{State: test.state, Merged: test.merged})
			if got != test.want {
				t.Errorf("pullState(state %q, merged %t) = %v, want %v", test.state, test.merged, got, test.want)
			}
			if fired := h.spy.times("UnknownEnumValue"); fired != 0 {
				t.Errorf("pullState(state %q) fired UnknownEnumValue %d time(s), want 0", test.state, fired)
			}
		})
	}
	t.Run("an_undeclared_pull_request_state_is_counted_and_named", func(t *testing.T) {
		h := newHarness(t, nil)
		if got := h.client.pullState(&wirePull{State: "future_state"}); got != forgeapi.PRStateUnknown {
			t.Errorf("pullState(%q) = %v, want %v", "future_state", got, forgeapi.PRStateUnknown)
		}
		if fired := h.spy.times("UnknownEnumValue"); fired != 1 {
			t.Errorf("pullState(%q) fired UnknownEnumValue %d time(s), want 1", "future_state", fired)
		}
		if !h.logged("future_state") {
			t.Errorf("pullState(%q) logged no line naming the value, want one", "future_state")
		}
	})
	t.Run("an_undeclared_issue_state_is_counted_and_named", func(t *testing.T) {
		h := newHarness(t, nil)
		if got := h.client.issueState("future_state"); got != forgeapi.IssueStateUnknown {
			t.Errorf("issueState(%q) = %v, want %v", "future_state", got, forgeapi.IssueStateUnknown)
		}
		if fired := h.spy.times("UnknownEnumValue"); fired != 1 {
			t.Errorf("issueState(%q) fired UnknownEnumValue %d time(s), want 1", "future_state", fired)
		}
		if !h.logged("future_state") {
			t.Errorf("issueState(%q) logged no line naming the value, want one", "future_state")
		}
	})
	t.Run("the_declared_issue_states_map", func(t *testing.T) {
		h := newHarness(t, nil)
		for value, want := range map[string]forgeapi.IssueState{
			"open":   forgeapi.IssueStateOpen,
			"closed": forgeapi.IssueStateClosed,
			"":       forgeapi.IssueStateUnknown,
		} {
			if got := h.client.issueState(value); got != want {
				t.Errorf("issueState(%q) = %v, want %v", value, got, want)
			}
		}
	})
}

// TestSignalReadsTheStructuredRateLimitHeader holds the budget signal to the one
// product of the two that sends it, read off the header rather than off a product
// marker, and to answering absent where nothing sent one.
func TestSignalReadsTheStructuredRateLimitHeader(t *testing.T) {
	for _, test := range []struct {
		name      string
		raw       string
		remaining int
		seconds   int
		ok        bool
	}{
		{name: "a_full_header", raw: "r=4990;t=60", remaining: 4990, seconds: 60, ok: true},
		{name: "remaining_alone", raw: "r=7", remaining: 7, ok: true},
		{name: "no_header_at_all", raw: ""},
		{name: "a_header_with_no_remaining", raw: "t=60"},
		{name: "a_header_whose_value_is_not_a_number", raw: "r=soon"},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{}
			if test.raw != "" {
				header.Set("RateLimit", test.raw)
			}
			remaining, reset, ok := signal(header)
			if ok != test.ok {
				t.Fatalf("signal(%q) = ok %t, want %t", test.raw, ok, test.ok)
			}
			if !ok {
				return
			}
			if remaining != test.remaining {
				t.Errorf("signal(%q) = remaining %d, want %d", test.raw, remaining, test.remaining)
			}
			if got := time.Until(reset).Round(time.Second); got != time.Duration(test.seconds)*time.Second {
				t.Errorf("signal(%q) = reset in %v, want %v", test.raw, got, time.Duration(test.seconds)*time.Second)
			}
		})
	}
}
