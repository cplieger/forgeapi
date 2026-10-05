package gitlab

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestMapperAnswersOneCellPerStatus holds this family's error mapping to one case per
// row of the table it implements, and it is the case a mapper replaced by a constant
// fails: every row states a different pair, so no single answer satisfies them.
//
// Some rows are one operation's rather than this product's, which is why the operation
// is read as well as the status: this product answers 400 to a merge sent without the
// head commit its own setting can make compulsory, 401 to a merge by a credential that
// may read the merge request and not merge it, and 404 to a cross-repository list
// scoped to a group it does not resolve or to a user's own namespace.
func TestMapperAnswersOneCellPerStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		op     string
		code   string
		status int
		kind   forgeapi.ErrorKind
	}{
		{
			name:   "400_on_a_merge_is_the_missing_head_commit",
			op:     opMergePR,
			status: http.StatusBadRequest,
			kind:   forgeapi.KindNotMergeable,
			code:   forgeapi.CodeMissingSHA,
		},
		{
			name:   "400_on_a_read_is_upstream_and_never_guessed",
			op:     opListIssues,
			status: http.StatusBadRequest,
			kind:   forgeapi.KindUpstream,
		},
		{
			name:   "401_on_a_merge_is_a_permission_rather_than_a_dead_credential",
			op:     opMergePR,
			status: http.StatusUnauthorized,
			kind:   forgeapi.KindForbidden,
			code:   forgeapi.CodeNoMergePermission,
		},
		{
			name:   "401_on_a_read_is_a_dead_credential",
			op:     "Whoami",
			status: http.StatusUnauthorized,
			kind:   forgeapi.KindUnauthorized,
		},
		{name: "403_is_forbidden", op: "ListPRs", status: http.StatusForbidden, kind: forgeapi.KindForbidden},
		{
			name:   "404_is_the_union_of_the_two_absences",
			op:     "ReadPR",
			status: http.StatusNotFound,
			kind:   forgeapi.KindNotFound,
			code:   forgeapi.CodeRepoOrPRNotVisible,
		},
		{
			name:   "404_on_a_per_repository_list_is_the_union_too",
			op:     opListIssues,
			status: http.StatusNotFound,
			kind:   forgeapi.KindNotFound,
			code:   forgeapi.CodeRepoOrPRNotVisible,
		},
		{
			name:   "404_on_the_cross_repository_pull_requests_is_the_unresolved_owner",
			op:     opListMyPRs,
			status: http.StatusNotFound,
			kind:   forgeapi.KindNotFound,
			code:   forgeapi.CodeOwnerUnresolved,
		},
		{
			name:   "404_on_the_cross_repository_issues_is_the_unresolved_owner",
			op:     opListMyIssues,
			status: http.StatusNotFound,
			kind:   forgeapi.KindNotFound,
			code:   forgeapi.CodeOwnerUnresolved,
		},
		{
			name:   "400_on_a_cross_repository_list_is_upstream_and_never_guessed",
			op:     opListMyPRs,
			status: http.StatusBadRequest,
			kind:   forgeapi.KindUpstream,
		},
		{
			name:   "405_is_a_refusal_to_merge",
			op:     opMergePR,
			status: http.StatusMethodNotAllowed,
			kind:   forgeapi.KindNotMergeable,
			code:   forgeapi.CodeNotMergeable,
		},
		{
			name:   "409_is_optimistic_concurrency_rather_than_a_content_conflict",
			op:     opMergePR,
			status: http.StatusConflict,
			kind:   forgeapi.KindConflict,
			code:   forgeapi.CodeStaleHead,
		},
		{
			name:   "422_is_a_branch_that_cannot_merge",
			op:     opMergePR,
			status: http.StatusUnprocessableEntity,
			kind:   forgeapi.KindNotMergeable,
			code:   forgeapi.CodeBranchCannotMerge,
		},
		{name: "429_is_a_throttle", op: "ListPRs", status: http.StatusTooManyRequests, kind: forgeapi.KindRateLimited},
		{name: "500_is_upstream", op: "ListPRs", status: http.StatusInternalServerError, kind: forgeapi.KindUpstream},
		{name: "502_is_upstream", op: "ReadPR", status: http.StatusBadGateway, kind: forgeapi.KindUpstream},
		{name: "an_unmapped_4xx_is_upstream", op: "ListPRs", status: http.StatusTeapot, kind: forgeapi.KindUpstream},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, code := mapper(test.op, test.status, http.Header{}, "")
			if kind != test.kind {
				t.Errorf("mapper(%s, %d) = kind %v, want %v", test.op, test.status, kind, test.kind)
			}
			if code != test.code {
				t.Errorf("mapper(%s, %d) = code %q, want %q", test.op, test.status, code, test.code)
			}
		})
	}
}

// TestAScopeErrorMemberMovesOnlyTheForbiddenCell holds the body's error member to the
// one cell that reads it: the member is upstream's, and on any other status the status
// decides, so a dead credential stays a reconnect and an absent project stays absent.
func TestAScopeErrorMemberMovesOnlyTheForbiddenCell(t *testing.T) {
	for _, test := range []struct {
		name      string
		op        string
		bodyError string
		code      string
		status    int
		kind      forgeapi.ErrorKind
	}{
		{
			name: "401_naming_a_scope_is_still_a_dead_credential", op: "ListPRs", bodyError: errorScope,
			status: http.StatusUnauthorized, kind: forgeapi.KindUnauthorized,
		},
		{
			name: "404_naming_a_fine_grained_permission_is_still_not_visible", op: "ReadPR", bodyError: errorGranularScope,
			status: http.StatusNotFound, kind: forgeapi.KindNotFound, code: forgeapi.CodeRepoOrPRNotVisible,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, code := mapper(test.op, test.status, http.Header{}, test.bodyError)
			if kind != test.kind || code != test.code {
				t.Errorf("mapper(%s, %d, %q) = (%v, %q), want (%v, %q)", test.op, test.status, test.bodyError, kind, code, test.kind, test.code)
			}
		})
	}
}

// TestOptimisticConcurrencyIsNotAContentConflict holds the one mapping cell whose
// whole reason for existing is that the same status means something else on the
// sibling family: a 409 here says the head moved under the caller, whose remedy is a
// re-read, and reporting a content conflict would send the caller to resolve a merge
// nothing conflicts in.
func TestOptimisticConcurrencyIsNotAContentConflict(t *testing.T) {
	h := newRefusingHarness(t, http.StatusConflict)
	_, err := h.client.MergePR(t.Context(), testRef(), testPR(), forgeapi.MergeRequest{HeadSHA: testHeadSHA})
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("MergePR against a 409 = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeStaleHead {
		t.Errorf("MergePR = code %q, want %q", fe.Code, forgeapi.CodeStaleHead)
	}
	if fe.Kind != forgeapi.KindConflict {
		t.Errorf("MergePR = kind %v, want %v", fe.Kind, forgeapi.KindConflict)
	}
	if fe.Status != http.StatusConflict {
		t.Errorf("MergePR = status %d, want %d: the real status is carried always", fe.Status, http.StatusConflict)
	}
}

// TestA409OffTheMergeIsNoStaleHead holds the 409 cells off the merge: a creation's 409
// is the merge request that already exists for the source branch, a request upstream
// rejected as invalid, and any other operation's 409 names no cause. Reporting either
// as the head having moved hands the caller a re-read for a state no re-read changes.
func TestA409OffTheMergeIsNoStaleHead(t *testing.T) {
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

// declaredEnums is what this product's own schema declares for the fields this
// family's totality tables map, cut from that schema into testdata.
type declaredEnums struct {
	Provenance string                  `json:"//"`
	Source     string                  `json:"source"`
	Enums      map[string]declaredEnum `json:"enums"`
	Fields     map[string]declaredFlag `json:"fields"`
}

type declaredEnum struct {
	DeclaredBy string   `json:"declared_by"`
	Enum       []string `json:"enum"`
}

type declaredFlag struct {
	Reason     string `json:"reason"`
	Deprecated bool   `json:"deprecated"`
}

// readDeclaredEnums loads that fixture, which is the specification side of every
// totality assertion below: a second hand-written list inside this file would hold the
// tables against a copy of themselves, so an upstream member addition would move
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
	switch {
	case out.Provenance == "":
		t.Fatal("Setup: the declared enumerations carry no provenance line, so nothing says which schema they were read from or when")
	case !strings.Contains(out.Source, "read 20"):
		t.Fatalf("Setup: the declared enumerations' source %q names no read date", out.Source)
	case len(out.Enums) == 0:
		t.Fatal("Setup: the declared enumerations carry no set, which is what every assertion here reads")
	}
	return out
}

// declared is one enumeration's members, failing when the fixture carries no such set
// rather than passing over an assertion with nothing on the other side.
func (d declaredEnums) declared(t *testing.T, name string) declaredEnum {
	t.Helper()
	set, ok := d.Enums[name]
	if !ok || len(set.Enum) == 0 {
		t.Fatalf("Setup: the declared enumerations carry no %q set", name)
	}
	return set
}

// lowered is the member set in the spelling the tables are keyed on, which is the REST
// one: both transports send the same members and only the case differs, so the tables
// fold the document's screaming snake onto this.
func (d declaredEnum) lowered() []string {
	out := make([]string, 0, len(d.Enum))
	for _, member := range d.Enum {
		out = append(out, strings.ToLower(member))
	}
	return out
}

// TestPipelineStatesMapEveryMemberTheSchemaDeclares holds the fold's table against the
// schema that declares the field, in both directions: a member the schema declares and
// the table does not folds to unknown on every response that sends it, and a member the
// table carries and the schema does not is a spelling this library invented.
//
// The two enumerations are asserted together because they are the same member set under
// two names, the pipeline's and the job's, and this family folds both through one
// table. If upstream ever splits them, the equality below is what fails.
func TestPipelineStatesMapEveryMemberTheSchemaDeclares(t *testing.T) {
	declared := readDeclaredEnums(t)
	pipeline := declared.declared(t, "pipeline_status")
	job := declared.declared(t, "job_status")
	for _, set := range []declaredEnum{pipeline, job} {
		for _, member := range set.lowered() {
			if _, ok := pipelineStates[member]; !ok {
				t.Errorf("pipelineStates carries no entry for %q, which %s declares", member, set.DeclaredBy)
			}
		}
	}
	if len(pipeline.Enum) != len(job.Enum) {
		t.Errorf("%s declares %d member(s) and %s declares %d, want the same set: this family folds both through one table",
			pipeline.DeclaredBy, len(pipeline.Enum), job.DeclaredBy, len(job.Enum))
	}
	if len(pipelineStates) != len(pipeline.Enum)+1 {
		t.Errorf("pipelineStates holds %d value(s), want %d: the set %s declares plus the empty string, which is this library's own member for a field that did not arrive",
			len(pipelineStates), len(pipeline.Enum)+1, pipeline.DeclaredBy)
	}
	// The fold's own arms, which no membership count states: the two verdicts a
	// consumer acts on, and the neutral one an ended-without-running job earns.
	for member, want := range map[string]forgeapi.CheckState{
		"success":  forgeapi.CheckPassing,
		"failed":   forgeapi.CheckFailing,
		"running":  forgeapi.CheckPending,
		"canceled": forgeapi.CheckNeutral,
	} {
		if got := pipelineStates[member]; got != want {
			t.Errorf("pipelineStates[%q] = %v, want %v", member, got, want)
		}
	}
}

// TestDetailedMergeStatusMapsEveryMemberTheSchemaDeclares holds the block reason's
// table against the schema, and holds the four members that are the status still being
// COMPUTED to the unknown answer: a reason invented from "not checked yet" is the wrong
// green this discipline exists to catch.
func TestDetailedMergeStatusMapsEveryMemberTheSchemaDeclares(t *testing.T) {
	declared := readDeclaredEnums(t)
	set := declared.declared(t, "detailed_merge_status")
	for _, member := range set.lowered() {
		if _, ok := detailedMergeStatuses[member]; !ok {
			t.Errorf("detailedMergeStatuses carries no entry for %q, which %s declares: an unmapped member reaches a consumer as an unexplained unknown",
				member, set.DeclaredBy)
		}
	}
	if len(detailedMergeStatuses) != len(set.Enum)+1 {
		t.Errorf("detailedMergeStatuses holds %d value(s), want %d: the set %s declares plus the empty string an instance too old to send the field leaves",
			len(detailedMergeStatuses), len(set.Enum)+1, set.DeclaredBy)
	}
	for member, want := range map[string]forgeapi.MergeBlockReason{
		"mergeable":        forgeapi.MergeBlockNone,
		"unchecked":        forgeapi.MergeBlockUnknown,
		"checking":         forgeapi.MergeBlockUnknown,
		"preparing":        forgeapi.MergeBlockUnknown,
		"draft_status":     forgeapi.MergeBlockDraft,
		"conflict":         forgeapi.MergeBlockConflicts,
		"need_rebase":      forgeapi.MergeBlockBehind,
		"ci_must_pass":     forgeapi.MergeBlockChecksFailing,
		"ci_still_running": forgeapi.MergeBlockChecksRunning,
		"not_approved":     forgeapi.MergeBlockBlocked,
	} {
		if got := detailedMergeStatuses[member]; got != want {
			t.Errorf("detailedMergeStatuses[%q] = %v, want %v", member, got, want)
		}
	}
}

// TestTheOlderMergeStatusIsMappedRatherThanDropped holds the one field of this product
// read over REST alone: the table carries every member the schema declares, so none
// goes uncounted, the detailed field wins where both are sent, and an answer carrying
// neither is unknown.
func TestTheOlderMergeStatusIsMappedRatherThanDropped(t *testing.T) {
	declared := readDeclaredEnums(t)
	set := declared.declared(t, "merge_status")
	for _, member := range set.lowered() {
		if _, ok := mergeStatuses[member]; !ok {
			t.Errorf("mergeStatuses carries no entry for %q, which %s declares", member, set.DeclaredBy)
		}
	}
	if len(mergeStatuses) != len(set.Enum)+1 {
		t.Errorf("mergeStatuses holds %d value(s), want %d", len(mergeStatuses), len(set.Enum)+1)
	}
	h := newHarness(t, nil)
	// The fallback is reached only where the detailed field is absent, and an
	// instance too old to send either answers the unknown member rather than none.
	for _, test := range []struct {
		detailed string
		older    string
		want     forgeapi.MergeBlockReason
	}{
		{detailed: "mergeable", older: "cannot_be_merged", want: forgeapi.MergeBlockNone},
		{older: "can_be_merged", want: forgeapi.MergeBlockNone},
		{older: "cannot_be_merged", want: forgeapi.MergeBlockConflicts},
		{older: "cannot_be_merged_recheck", want: forgeapi.MergeBlockUnknown},
		{want: forgeapi.MergeBlockUnknown},
	} {
		got := h.client.blockReason("detailed merge status (rest)", test.detailed, test.older)
		if got != test.want {
			t.Errorf("blockReason(%q, %q) = %v, want %v", test.detailed, test.older, got, test.want)
		}
	}
}

// TestTheDocumentsSelectNoDeprecatedFieldOrMember holds both documents against the
// schema's own deprecation flags, which is the gate a document has to clear before it
// can ship: a deprecated field still ANSWERS, so nothing about executing it fails, and
// the flag is the only warning there is.
//
// It is also the case that records why no merge-train field is selected. The train
// connection and its cars are what a queue verdict on this product would have to read,
// and both carry a deprecation reason, so the queue state is the neutral member rather
// than a value read from an experiment.
func TestTheDocumentsSelectNoDeprecatedFieldOrMember(t *testing.T) {
	declared := readDeclaredEnums(t)
	if len(declared.Fields) == 0 {
		t.Fatal("Setup: the declared enumerations carry no field flags, which is what this case reads")
	}
	selected := 0
	for name, flag := range declared.Fields {
		_, field, ok := strings.Cut(name, ".")
		if !ok {
			t.Errorf("Setup: the field flag %q names no type and field", name)
			continue
		}
		inDocument := strings.Contains(prList.text, field) || strings.Contains(prRead.text, field)
		if !inDocument {
			continue
		}
		selected++
		if flag.Deprecated {
			t.Errorf("a document selects %s, which this product's schema flags deprecated (%q): a deprecated field still answers, so the flag is the only warning there is",
				name, flag.Reason)
		}
	}
	if selected == 0 {
		t.Error("no field flag matched either document, so this case asserted nothing; the flags and the documents have to name the same fields")
	}
	for _, name := range []string{"Project.mergeTrains", "MergeTrain.cars"} {
		flag, ok := declared.Fields[name]
		if !ok {
			t.Errorf("Setup: no flag for %s, which is the field a queue verdict on this product would read", name)
			continue
		}
		if !flag.Deprecated {
			t.Errorf("%s is no longer deprecated, so the reason this family answers the neutral queue member has expired and the expectation table owes a merge-train row",
				name)
		}
		if strings.Contains(prList.text, "mergeTrains") || strings.Contains(prRead.text, "mergeTrains") {
			t.Errorf("a document selects mergeTrains, which %s flags deprecated: the schema gate refuses one", name)
		}
	}
}

// TestPullAndIssueStatesMapEveryMemberTheSchemaDeclares holds the two item-state
// tables against the schema, including the FILTER member that can never be one row's
// state: leaving it out would make a table this discipline calls total incomplete, and
// mapping it to a state would publish a filter as an answer.
func TestPullAndIssueStatesMapEveryMemberTheSchemaDeclares(t *testing.T) {
	declared := readDeclaredEnums(t)
	pulls := declared.declared(t, "merge_request_state")
	issues := declared.declared(t, "issue_state")
	for _, member := range pulls.lowered() {
		if _, ok := prStates[member]; !ok {
			t.Errorf("prStates carries no entry for %q, which %s declares", member, pulls.DeclaredBy)
		}
	}
	for _, member := range issues.lowered() {
		if _, ok := issueStates[member]; !ok {
			t.Errorf("issueStates carries no entry for %q, which %s declares", member, issues.DeclaredBy)
		}
	}
	if len(prStates) != len(pulls.Enum)+1 {
		t.Errorf("prStates holds %d value(s), want %d", len(prStates), len(pulls.Enum)+1)
	}
	if len(issueStates) != len(issues.Enum)+1 {
		t.Errorf("issueStates holds %d value(s), want %d", len(issueStates), len(issues.Enum)+1)
	}
	if got := prStates["all"]; got != forgeapi.PRStateUnknown {
		t.Errorf("prStates[all] = %v, want %v: it is a filter member and no row's state", got, forgeapi.PRStateUnknown)
	}
	if got := prStates["locked"]; got != forgeapi.PRStateOpen {
		t.Errorf("prStates[locked] = %v, want %v: a locked merge request is open and mid-merge", got, forgeapi.PRStateOpen)
	}
}

// TestAnUnmappedMemberIsCountedAndNamed holds the three things the unknown arm of every
// totality table owes: the unknown member, the counter, and a log line NAMING the value,
// which is what tells a maintainer the upstream enumeration grew rather than leaving an
// unknown with no cause.
//
// The field carries the TRANSPORT as well as the enumeration, because this product
// sends the same members in two cases and which one grew is the first thing a
// maintainer needs.
func TestAnUnmappedMemberIsCountedAndNamed(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.client.checkState("pipeline status (document)", "TELEPORTED"); got != forgeapi.CheckUnknown {
		t.Errorf("checkState(TELEPORTED) = %v, want %v", got, forgeapi.CheckUnknown)
	}
	if h.spy.times("UnknownEnumValue:pipeline status (document)") != 1 {
		t.Error("an unmapped pipeline status fired no counter naming the document transport, so an enumeration that grew upstream is invisible")
	}
	if !h.logged("TELEPORTED") {
		t.Error("an unmapped pipeline status logged no line naming the value, so a maintainer cannot tell which member arrived")
	}
}

// TestAMutationsUnmappedMergeStatusIsCountedAndNamed holds the totality table's
// unknown arm on the path that discards what the table answers: a mutation's own
// answer names no block reason whatever status it carries, and a member the table
// does not carry is still counted and named there, so an enumeration that grew
// upstream is as visible on a mutation as on a read.
func TestAMutationsUnmappedMergeStatusIsCountedAndNamed(t *testing.T) {
	body := strings.Replace(restMergeRequestBody, `"detailed_merge_status": "mergeable"`, `"detailed_merge_status": "teleported"`, 1)
	if body == restMergeRequestBody {
		t.Fatal("Setup: the merge-request body carries no mergeable detailed status to replace")
	}
	h := newHarness(t, map[string]string{"POST /api/v4/projects/" + testEncoded + "/merge_requests": body})
	got, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{
		Title: "Example pull request", SourceBranch: "example-feature", TargetBranch: "main",
	})
	if err != nil {
		t.Fatalf("CreatePR answering detailed_merge_status %q = %v, want the pull request", "teleported", err)
	}
	if got.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
		t.Errorf("CreatePR answering detailed_merge_status %q: Action.MergeBlocked = %v, want %v", "teleported", got.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:detailed merge status (rest)"); n != 1 {
		t.Errorf("CreatePR answering detailed_merge_status %q fired the REST detailed-status counter %d time(s), want 1", "teleported", n)
	}
	if !h.logged("teleported") {
		t.Errorf("CreatePR answering detailed_merge_status %q logged no line naming the value", "teleported")
	}
}

// TestTheCrossRepositoryListsUnmappedMergeStatusIsCountedAndNamed holds the same
// unknown arm on the cross-repository list, whose rows name no block reason whatever
// status they carry: a member the table does not carry is still counted and named
// there, so an enumeration that grew upstream is as visible on the list as on a
// mutation.
func TestTheCrossRepositoryListsUnmappedMergeStatusIsCountedAndNamed(t *testing.T) {
	body := strings.Replace(restMergeRequestBody, `"detailed_merge_status": "mergeable"`, `"detailed_merge_status": "teleported"`, 1)
	if body == restMergeRequestBody {
		t.Fatal("Setup: the merge-request body carries no mergeable detailed status to replace")
	}
	h := newHarness(t, map[string]string{myMergeRoute: "[" + body + "]"})
	page, err := h.client.ListMyPRs(t.Context())
	if err != nil {
		t.Fatalf("ListMyPRs answering detailed_merge_status %q = %v, want the page", "teleported", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListMyPRs answering detailed_merge_status %q = %d row(s), want 1", "teleported", len(page.Items))
	}
	if got := page.Items[0].Action.MergeBlocked; got != forgeapi.MergeBlockUnknown {
		t.Errorf("ListMyPRs answering detailed_merge_status %q: row 0 Action.MergeBlocked = %v, want %v", "teleported", got, forgeapi.MergeBlockUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:detailed merge status (rest)"); n != 1 {
		t.Errorf("ListMyPRs answering detailed_merge_status %q fired the REST detailed-status counter %d time(s), want 1", "teleported", n)
	}
	if !h.logged("teleported") {
		t.Errorf("ListMyPRs answering detailed_merge_status %q logged no line naming the value", "teleported")
	}
}

// TestAnUnmappedValueIsLoggedSanitizedWithinABound holds the line naming an unmapped
// value to what a line carrying upstream text owes a consumer's handler: a JSON
// handler emits a bidi override and a C1 control raw, so neither may reach it, and
// an instance can send a member of any length, so the line carries a bounded prefix
// rather than the whole. The member stays recognisable. It is driven through the
// cross-repository list, whose rows are looked up though they name no block reason.
func TestAnUnmappedValueIsLoggedSanitizedWithinABound(t *testing.T) {
	hostile := "teleported\u202e\u0085" + strings.Repeat("x", 4096)
	quoted, err := json.Marshal(hostile)
	if err != nil {
		t.Fatalf("Setup: quoting the member: %v", err)
	}
	body := strings.Replace(restMergeRequestBody, `"detailed_merge_status": "mergeable"`, `"detailed_merge_status": `+string(quoted), 1)
	if body == restMergeRequestBody {
		t.Fatal("Setup: the merge-request body carries no mergeable detailed status to replace")
	}
	var logs bytes.Buffer
	h := newHarness(t, map[string]string{myMergeRoute: "[" + body + "]"},
		forgeapi.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	if _, err := h.client.ListMyPRs(t.Context()); err != nil {
		t.Fatalf("ListMyPRs answering an unmapped detailed_merge_status = %v, want the page", err)
	}
	var record string
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, `"msg":"forgeapi unmapped enumeration value"`) {
			record = line
			break
		}
	}
	if record == "" {
		t.Fatalf("ListMyPRs answering an unmapped detailed_merge_status logged no unmapped-value record; the JSON log is %q", logs.String())
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

// TestEveryWireTypeDecodesTheBytesTheProductSent is the wire-shape gate, and it reads
// the RECORDED bodies rather than a shape written here.
//
// A tag that disagrees with what the instance sends is the defect class this case
// exists for, and it is invisible to a test whose fixture the same author wrote: the
// labels key is the worked example, since this product answers it as an array of
// objects on a read that asked for the details and as an array of names on a
// mutation's answer, so a type stating either shape alone decodes nothing on the other.
func TestEveryWireTypeDecodesTheBytesTheProductSent(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, nil)

	var project docProject
	recorded.row(t, "graphql_pr_read", &project)
	node := project.MergeRequest
	if node == nil {
		t.Fatal("the recorded document body decoded no merge request, so every field assertion below would pass over nothing")
	}
	if node.IID == "" {
		t.Error("the recorded document body decoded an empty iid: this product answers the number as a STRING over this transport and as an integer over REST, which is why the two wire types are declared apart")
	}
	if node.DiffHeadSHA == "" {
		t.Error("the recorded document body decoded no diff head, which is the commit a merge and a re-run pin themselves to")
	}
	if node.Labels == nil || len(node.Labels.Nodes) == 0 || node.Labels.Nodes[0].Title == "" {
		t.Error("the recorded document body decoded no label title: this transport spells the member `title` and not `name`")
	}
	if node.HeadPipeline == nil || node.HeadPipeline.Status == "" {
		t.Error("the recorded document body decoded no head-pipeline status, which is the whole of the folded verdict on this product")
	}
	if node.DetailedMergeStatus == "" {
		t.Error("the recorded document body decoded no detailed merge status, which is the block reason's source")
	}
	if project.UserPermissions == nil {
		t.Error("the recorded document body decoded no project permissions, which is what the grant accessor answers from at no request of its own")
	}
	if got := h.client.checkState("pipeline status (document)", node.HeadPipeline.Status); got == forgeapi.CheckUnknown {
		t.Errorf("the recorded head-pipeline status %q folded to unknown, so the table does not carry the member this instance sent", node.HeadPipeline.Status)
	}

	var mr restMergeRequest
	recorded.row(t, "rest_merge_request", &mr)
	if mr.IID == 0 {
		t.Error("the recorded REST merge request decoded no iid: this transport answers an INTEGER where the document answers a string")
	}
	if mr.SHA == "" || mr.References == nil || mr.References.Full == "" {
		t.Error("the recorded REST merge request decoded no head or no full reference, and the reference is what a cross-repository row's own project is recovered from")
	}
	if labels := normalizeRESTLabels(mr.Labels); len(labels) == 0 || labels[0].Name == "" {
		t.Error("the recorded REST merge request decoded no label name from the NAMES form of that key, which is what a mutation's answer gives")
	}
	if got := h.client.blockReason("detailed merge status (rest)", mr.DetailedMergeStatus, mr.MergeStatus); got == forgeapi.MergeBlockUnknown {
		t.Errorf("the recorded detailed merge status %q mapped to unknown, so the table does not carry the member this instance sent", mr.DetailedMergeStatus)
	}

	var issue restIssue
	recorded.row(t, "rest_issue", &issue)
	labels := normalizeRESTLabels(issue.Labels)
	if len(labels) == 0 || labels[0].Name == "" || labels[0].Color == "" || labels[0].Description == "" {
		t.Errorf("the recorded issue decoded %v from the OBJECT form of the labels key, want a name, a colour and a description: this product answers the objects under that one key and declares no separate detail key",
			labels)
	}

	var project2 restProject
	recorded.row(t, "rest_project", &project2)
	if project2.PathWithNamespace == "" || project2.CloneURL == "" || project2.LastActivityAt.IsZero() {
		t.Error("the recorded project decoded no namespace path, clone URL or activity time")
	}
	if strings.Count(project2.PathWithNamespace, "/") < 2 {
		t.Errorf("the recorded project's path is %q, want a NESTED one: this family's whole selector shape is that a path can carry more than one separator", project2.PathWithNamespace)
	}

	var release restRelease
	recorded.row(t, "rest_release", &release)
	if normalizeRelease(&release).WebURL == "" {
		t.Error("the recorded release normalized no web URL, which this product answers under the links object rather than as a key of its own")
	}
	if release.ReleasedAt.IsZero() {
		t.Error("the recorded release decoded no publication time, which this product spells released_at")
	}

	var pipeline restPipeline
	recorded.row(t, "rest_pipeline", &pipeline)
	if pipeline.ID == 0 || pipeline.SHA == "" {
		t.Error("the recorded pipeline decoded no identifier or no commit, and the identifier is what the re-run verb is addressed by")
	}
}

// TestTheBudgetSignalIsReadFromEveryResponse holds the signal this product rides on
// every response, a refusal included, which is what makes the governor's figure
// cost-free: a reserve held against a figure nobody read would read as full at the
// moment the quota was gone.
func TestTheBudgetSignalIsReadFromEveryResponse(t *testing.T) {
	header := http.Header{
		"Ratelimit-Remaining": {"4990"},
		"Ratelimit-Reset":     {"1790000000"},
	}
	remaining, reset, ok := signal(header)
	if !ok {
		t.Fatal("signal read no figure from the headers this product sends on every response")
	}
	if remaining != 4990 {
		t.Errorf("signal = remaining %d, want 4990", remaining)
	}
	if reset.IsZero() || reset.Unix() != 1790000000 {
		t.Errorf("signal = reset %v, want the epoch second the header carries", reset)
	}
	if _, _, ok := signal(http.Header{}); ok {
		t.Error("signal read a figure from a response carrying no header, so a connection that has seen no answer would publish a budget it never read")
	}
}

// TestThePushAffordanceReadsTheRoleInteger holds the one affordance this product
// answers as a NUMBER rather than a flag, which is why it is three-valued: a record
// carrying no permissions at all is a caller who cannot see the grant, not one who
// cannot push.
func TestThePushAffordanceReadsTheRoleInteger(t *testing.T) {
	for _, test := range []struct {
		name string
		in   *restPermissions
		want forgeapi.Support
	}{
		{name: "no_permissions_object_is_unknown", want: forgeapi.SupportUnknown},
		{name: "both_grants_null_is_unknown", in: &restPermissions{}, want: forgeapi.SupportUnknown},
		{
			name: "a_reporter_cannot_push",
			in:   &restPermissions{ProjectAccess: &restAccess{AccessLevel: 20}},
			want: forgeapi.SupportNo,
		},
		{
			name: "a_developer_can",
			in:   &restPermissions{ProjectAccess: &restAccess{AccessLevel: developerAccess}},
			want: forgeapi.SupportYes,
		},
		{
			name: "the_higher_of_the_two_grants_decides",
			in: &restPermissions{
				ProjectAccess: &restAccess{AccessLevel: 10},
				GroupAccess:   &restAccess{AccessLevel: 40},
			},
			want: forgeapi.SupportYes,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := pushSupport(test.in); got != test.want {
				t.Errorf("pushSupport(%+v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}
