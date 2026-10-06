package github

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi"
)

// TestEveryWireTypeDecodesTheRecordedBytes holds each of this family's wire types
// against the bytes the product actually sent, which is the one instrument that can
// fail on a tag that disagrees with the schema.
//
// A shape written out in this file would hold the types against a copy of themselves,
// so the fixture is a TRIMMED RECORDING rather than an authored body: every row under
// testdata/captures.json is cut from recorded responses of this product's own API.
func TestEveryWireTypeDecodesTheRecordedBytes(t *testing.T) {
	recorded := readCaptures(t)
	for _, test := range []struct {
		into any
		row  string
	}{
		{row: "graphql_pr_read", into: &docRepository{}},
		{row: "graphql_search_row", into: &docPullRequest{}},
		{row: "graphql_issue_search_row", into: &docIssue{}},
		{row: "rest_workflow_runs_listing", into: &restWorkflowRuns{}},
		{row: "rest_pull", into: &restPull{}},
		{row: "rest_issue", into: &restIssue{}},
		{row: "rest_issue_that_is_a_pull_request", into: &restIssue{}},
		{row: "rest_repo", into: &restRepo{}},
		{row: "rest_repo_listing_row", into: &restRepo{}},
		{row: "rest_release", into: &restRelease{}},
		{row: "rest_release_draft", into: &restRelease{}},
		{row: "rest_combined_status_on_an_actions_repository", into: &restCombined{}},
		{row: "rest_combined_status_with_real_statuses", into: &restCombined{}},
		{row: "rest_check_runs", into: &restCheckRuns{}},
		{row: "rest_workflow_runs", into: &restWorkflowRuns{}},
		{row: "rest_merge_accepted", into: &restMergeAnswer{}},
		{row: "rest_merge_handle_read", into: &restMergeAnswer{}},
		{row: "rest_user", into: &restUser{}},
		{row: "rest_meta_appliance", into: &restMeta{}},
	} {
		t.Run(test.row, func(t *testing.T) {
			recorded.row(t, test.row, test.into)
		})
	}
}

// TestTheRecordedPullRequestDecodesEveryFieldTheContractPublishes holds the document
// arm's normalizer to the recorded bytes field by field. A decode that merely succeeds
// proves nothing about the TAGS: a struct whose every tag is misspelled decodes a
// wholly zero value with no error at all.
func TestTheRecordedPullRequestDecodesEveryFieldTheContractPublishes(t *testing.T) {
	var repo docRepository
	readCaptures(t).row(t, "graphql_pr_read", &repo)
	if repo.NameWithOwner != testSelector {
		t.Errorf("nameWithOwner = %q, want %q", repo.NameWithOwner, testSelector)
	}
	if repo.PullRequest == nil {
		t.Fatal("the recorded repository carries no pullRequest, which is the selection this read is for")
	}
	h := newHarness(t, nil)
	got := h.client.normalizeDocPull(repo.PullRequest, testRef())
	for _, check := range []struct {
		got  any
		want any
		name string
	}{
		{name: "Ref.Number", got: got.Ref.Number, want: 1},
		{name: "Ref.Sigil", got: got.Ref.Sigil, want: "#"},
		{name: "Title", got: got.Title, want: "Example pull request"},
		{name: "Body", got: got.Body, want: "Example body."},
		{name: "Author", got: got.Author, want: "example-user"},
		{name: "SourceBranch", got: got.SourceBranch, want: "example-feature"},
		{name: "TargetBranch", got: got.TargetBranch, want: "main"},
		{name: "HeadSHA", got: got.HeadSHA, want: testHeadSHA},
		{name: "State", got: got.State, want: forgeapi.PRStateOpen},
		{name: "Draft", got: got.Draft, want: false},
		// The recorded pull request is the measurement that makes mergeability
		// three-valued: it answered MERGEABLE beside BLOCKED, so the two fields
		// answer different questions and a family folding them into one would
		// report it ready to merge.
		{name: "Action.Mergeable", got: got.Action.Mergeable, want: forgeapi.SupportYes},
		{name: "Action.MergeBlocked", got: got.Action.MergeBlocked, want: forgeapi.MergeBlockBlocked},
		// Its auto-merge request is a non-null OBJECT on the recorded bytes, which
		// is the armed answer: the presence is the flag, so a normalizer reading a
		// boolean out of that key would answer no on every armed pull request.
		{name: "Action.AutoMergeArmed", got: got.Action.AutoMergeArmed, want: forgeapi.SupportYes},
		{name: "Action.QueueState", got: got.Action.QueueState, want: forgeapi.QueueNone},
		{name: "Action.QueuePosition", got: got.Action.QueuePosition, want: forgeapi.QueuePositionUnknown},
		{name: "Action.Checks", got: got.Action.Checks, want: forgeapi.CheckPassing},
		{name: "Action.ChecksPassing", got: got.Action.ChecksPassing, want: 2},
		{name: "Action.ChecksTotal", got: got.Action.ChecksTotal, want: 2},
		{name: "Labels[0].Name", got: got.Labels[0].Name, want: "example-label"},
		{name: "Labels[0].Color", got: got.Labels[0].Color, want: "ededed"},
		{name: "Labels[0].Description", got: got.Labels[0].Description, want: "Example label."},
	} {
		if check.got != check.want {
			t.Errorf("normalizeDocPull(the recorded bytes).%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if got.WebURL == "" || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("normalizeDocPull(the recorded bytes) = web %q, created %v, updated %v, want all three populated",
			got.WebURL, got.CreatedAt, got.UpdatedAt)
	}
}

// TestTheRecordedIssueSearchRowDecodesEveryFieldTheContractPublishes holds the issue
// document's normalizer to the recorded row field by field, for the reason the pull
// request's own case gives: a struct whose every tag is misspelled decodes a wholly
// zero value with no error.
func TestTheRecordedIssueSearchRowDecodesEveryFieldTheContractPublishes(t *testing.T) {
	var row docIssue
	readCaptures(t).row(t, "graphql_issue_search_row", &row)
	h := newHarness(t, nil)
	got := h.client.normalizeDocIssue(&row)
	labels := []forgeapi.Label{{Name: "example-label", Color: "ededed", Description: "Example label."}}
	for _, check := range []struct {
		got  any
		want any
		name string
	}{
		{name: "Ref.Number", got: got.Ref.Number, want: 1},
		{name: "Repo", got: got.Repo, want: testRef()},
		{name: "Title", got: got.Title, want: "Example issue"},
		{name: "Body", got: got.Body, want: "Example issue body."},
		{name: "Author", got: got.Author, want: "example-user"},
		{name: "WebURL", got: got.WebURL, want: "https://forge.example/example/example/issues/1"},
		{name: "Labels", got: slices.Equal(got.Labels, labels), want: true},
		{name: "CreatedAt", got: got.CreatedAt.Format(time.RFC3339), want: "2026-01-01T00:00:00Z"},
		{name: "UpdatedAt", got: got.UpdatedAt.Format(time.RFC3339), want: "2026-01-02T00:00:00Z"},
		{name: "State", got: got.State, want: forgeapi.IssueStateOpen},
	} {
		if check.got != check.want {
			t.Errorf("normalizeDocIssue(the recorded row).%s = %v, want %v", check.name, check.got, check.want)
		}
	}
	if n := h.spy.times("UnknownEnumValue:issue state (document)"); n != 0 {
		t.Errorf("normalizeDocIssue(the recorded row) counted %d unmapped state(s), want 0: OPEN is a member", n)
	}
}

// TestABoundedFoldIsExactFromTheConnectionsOwnCounts is the measurement the exact
// bounded fold rests on, held against the recorded bytes: the search document's row answered SEVEN
// contexts over an EMPTY node list, because the counts describe the whole collection
// and the selected page carries one row at most.
//
// So a row whose names are truncated still answers a real verdict and real counts, and
// a fold that counted the NODES would answer a total of zero here while looking green.
func TestABoundedFoldIsExactFromTheConnectionsOwnCounts(t *testing.T) {
	var row docPullRequest
	readCaptures(t).row(t, "graphql_search_row", &row)
	h := newHarness(t, nil)
	got := h.client.foldRollup(rollupOf(&row))
	if got.total != 7 {
		t.Errorf("foldRollup(the recorded search row).total = %d, want 7: the connection's own counts describe the collection, not the page", got.total)
	}
	if got.passing != 4 || got.neutral != 3 {
		t.Errorf("foldRollup(the recorded search row) = %d passing and %d neutral, want 4 and 3: SKIPPED is a neutral conclusion and the fold reads the conclusion half",
			got.passing, got.neutral)
	}
	if got.state != forgeapi.CheckPassing {
		t.Errorf("foldRollup(the recorded search row).state = %v, want %v", got.state, forgeapi.CheckPassing)
	}
	if len(got.contexts) != 0 {
		t.Errorf("foldRollup(the recorded search row) carries %d context row(s), want 0: the recorded page selected none, which is what makes the counts the only source", len(got.contexts))
	}
	if !got.exact {
		t.Error("foldRollup(the recorded search row) is not marked exact, want it: a further page must not add these counts again")
	}
	if sum := got.passing + got.failing + got.pending + got.neutral + got.unknown; sum != got.total {
		t.Errorf("the per-member counts sum to %d, want the total %d", sum, got.total)
	}
}

// TestAnEmptyRollupIsNotAPassingFold is the hazard this product's own REST combined
// status cannot avoid, held on the source that can: a commit with nothing to fold
// answers the unknown member with a zero total, never a green.
func TestAnEmptyRollupIsNotAPassingFold(t *testing.T) {
	h := newHarness(t, nil)
	got := h.client.foldRollup(nil)
	if got.state != forgeapi.CheckUnknown {
		t.Errorf("foldRollup(a null rollup).state = %v, want %v: a verdict over no evidence is one this library invented", got.state, forgeapi.CheckUnknown)
	}
	if got.total != 0 || got.passing != 0 {
		t.Errorf("foldRollup(a null rollup) = total %d and %d passing, want zero of each", got.total, got.passing)
	}
}

// TestTheCombinedStatusStateIsNeverTheVerdict is the same hazard measured on the REST
// arm's own bytes: on a repository whose CI is Actions that endpoint answers a state of
// pending over an EMPTY statuses array, byte-identical to a commit with no CI at all,
// so the library must fold the rows it holds rather than read that field.
func TestTheCombinedStatusStateIsNeverTheVerdict(t *testing.T) {
	recorded := readCaptures(t)
	for _, row := range []string{
		"rest_combined_status_on_an_actions_repository",
		"rest_combined_status_on_a_commit_with_no_ci",
	} {
		t.Run(row, func(t *testing.T) {
			var combined restCombined
			recorded.row(t, row, &combined)
			if len(combined.Statuses) != 0 {
				t.Fatalf("the recorded %s carries %d status row(s), want none: this case is about the empty answer", row, len(combined.Statuses))
			}
			if combined.State == "" {
				t.Fatalf("the recorded %s carries no state, want the field this case exists to refuse reading", row)
			}
			var folded fold
			folded.state = folded.verdict()
			if folded.state != forgeapi.CheckUnknown {
				t.Errorf("a fold over %s = %v, want %v: this endpoint's own state is %q over nothing at all",
					row, folded.state, forgeapi.CheckUnknown, combined.State)
			}
		})
	}
}

// enumerations is the schema's own member sets, recorded so a table here is held
// against what the product declares rather than against what this file remembers.
type enumerations struct {
	Provenance string `json:"//"`
	Source     string `json:"source"`
	Enums      map[string]struct {
		DeclaredBy string `json:"declared_by"`
		Enum       []struct {
			Name       string `json:"name"`
			Deprecated bool   `json:"deprecated"`
		} `json:"enum"`
	} `json:"enums"`
}

func readEnumerations(t *testing.T) enumerations {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "enumerations.json"))
	if err != nil {
		t.Fatalf("Setup: reading the recorded enumerations: %v", err)
	}
	var out enumerations
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Setup: decoding the recorded enumerations: %v", err)
	}
	if !strings.Contains(out.Provenance, "read 20") {
		t.Fatalf("Setup: the recorded enumerations' provenance %q names no read date", out.Provenance)
	}
	if len(out.Enums) == 0 {
		t.Fatal("Setup: the recorded enumerations carry no row")
	}
	return out
}

// TestEveryEnumerationTheSchemaDeclaresIsMapped is the totality discipline's own gate:
// each member the schema declares has a row in the table that reads it, so a member
// added upstream fails here rather than arriving as an unknown nobody noticed.
//
// The tables are keyed on the LOWER-SNAKE spelling, because both transports send the
// same member sets in two cases and the family folds a document's screaming-snake value
// onto the REST one before the lookup.
func TestEveryEnumerationTheSchemaDeclaresIsMapped(t *testing.T) {
	declared := readEnumerations(t)
	for _, test := range []struct {
		mapped func(string) bool
		row    string
	}{
		{row: "status_state", mapped: func(m string) bool { _, ok := statusStates[m]; return ok }},
		{row: "check_conclusion_state", mapped: func(m string) bool { _, ok := checkRunStates[m]; return ok }},
		{row: "check_status_state", mapped: func(m string) bool { _, ok := checkRunStates[m]; return ok }},
		{row: "check_run_state", mapped: func(m string) bool { _, ok := checkRunStates[m]; return ok }},
		{row: "merge_state_status", mapped: func(m string) bool { _, ok := mergeStateStatuses[m]; return ok }},
		{row: "mergeable_state", mapped: func(m string) bool { _, ok := mergeableStates[m]; return ok }},
		{row: "merge_queue_entry_state", mapped: func(m string) bool { _, ok := queueStates[m]; return ok }},
		{row: "pull_request_state", mapped: func(m string) bool { _, ok := prStates[m]; return ok }},
		{row: "merge_method", mapped: func(m string) bool { return slices.Contains(mergeMethods, m) }},
	} {
		t.Run(test.row, func(t *testing.T) {
			row, ok := declared.Enums[test.row]
			if !ok {
				t.Fatalf("the recorded enumerations carry no %q row", test.row)
			}
			if len(row.Enum) == 0 {
				t.Fatalf("%s declares no member, which is what this case iterates", row.DeclaredBy)
			}
			for _, m := range row.Enum {
				if !test.mapped(member(m.Name)) {
					t.Errorf("%s declares %s and this family's table carries no row for it: an unmapped member answers the unknown value with no cause named",
						row.DeclaredBy, m.Name)
				}
			}
		})
	}
}

// TestTheDeprecatedMergeStateMemberIsMappedToo holds the one member of this product's
// enumerations that carries a deprecation reason. It is visible only to an
// introspection asking for deprecated members explicitly, so a gate reading the default
// view could never see it, and a table missing it would answer unknown for every draft
// pull request on an instance that still sends it.
func TestTheDeprecatedMergeStateMemberIsMappedToo(t *testing.T) {
	declared := readEnumerations(t)
	row := declared.Enums["merge_state_status"]
	found := false
	for _, m := range row.Enum {
		if !m.Deprecated {
			continue
		}
		found = true
		reason, ok := mergeStateStatuses[member(m.Name)]
		if !ok {
			t.Errorf("%s declares the deprecated member %s and the table carries no row for it", row.DeclaredBy, m.Name)
			continue
		}
		if reason == forgeapi.MergeBlockUnknown {
			t.Errorf("the deprecated member %s maps to %v, want a real reason: an instance still sending it has a draft pull request rather than an unreadable one",
				m.Name, reason)
		}
	}
	if !found {
		t.Fatalf("%s declares no deprecated member, which is what this case exists for; re-read the schema with deprecated members asked for explicitly", row.DeclaredBy)
	}
}

// TestAnUnmappedEnumerationValueIsCountedAndNamed holds the other half of the totality
// discipline: a value no table carries answers the unknown member, fires the counter
// and is LOGGED with its value, which is what tells a maintainer the enumeration grew.
func TestAnUnmappedEnumerationValueIsCountedAndNamed(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.client.checkState("status context state (document)", "TELEPORTED"); got != forgeapi.CheckUnknown {
		t.Errorf("checkState(%q) = %v, want %v", "TELEPORTED", got, forgeapi.CheckUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:status context state (document)"); n != 1 {
		t.Errorf("the unknown-enumeration counter fired %d time(s), want 1", n)
	}
	if !h.logged("TELEPORTED") {
		t.Error("the unmapped value is not in the log, want it named: an unknown with no cause is what this rule exists to prevent")
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
	if got := h.client.checkState("status context state (document)", hostile); got != forgeapi.CheckUnknown {
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

// TestTheMergeStateTableSeparatesMergeableFromBlocked is the measurement that makes
// mergeability three-valued rather than a boolean: this product answered MERGEABLE
// beside BLOCKED on one pull request, so the two fields answer different questions and
// a family folding them into one would report a mergeable pull request as ready.
func TestTheMergeStateTableSeparatesMergeableFromBlocked(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.client.mergeableSupport("mergeable (document)", "MERGEABLE"); got != forgeapi.SupportYes {
		t.Errorf("mergeableSupport(MERGEABLE) = %v, want %v", got, forgeapi.SupportYes)
	}
	if got := h.client.blockReason("merge state status (document)", "BLOCKED"); got != forgeapi.MergeBlockBlocked {
		t.Errorf("blockReason(BLOCKED) = %v, want %v", got, forgeapi.MergeBlockBlocked)
	}
	if got := h.client.blockReason("merge state status (document)", "HAS_HOOKS"); got != forgeapi.MergeBlockNone {
		t.Errorf("blockReason(HAS_HOOKS) = %v, want %v: that member is a mergeable state, so reporting it as a block disables a control the forge would honour",
			got, forgeapi.MergeBlockNone)
	}
}

// TestACheckRunsConclusionIsWhatMakesNeutralReachable holds the check state's source at
// the mapper: the rollup's own state enumeration has no neutral and no skipped member, so a fold
// that read it could never answer neutral, and both live in the per-run conclusion.
func TestACheckRunsConclusionIsWhatMakesNeutralReachable(t *testing.T) {
	declared := readEnumerations(t)
	for _, m := range declared.Enums["status_state"].Enum {
		if state := statusStates[member(m.Name)]; state == forgeapi.CheckNeutral {
			t.Fatalf("%s declares %s, which this family maps to %v: if the rollup's own enumeration carried a neutral member the reason for reading the contexts would be gone",
				declared.Enums["status_state"].DeclaredBy, m.Name, state)
		}
	}
	h := newHarness(t, nil)
	for _, conclusion := range []string{"NEUTRAL", "SKIPPED"} {
		if got := h.client.runState("check run", "COMPLETED", conclusion); got != forgeapi.CheckNeutral {
			t.Errorf("runState(COMPLETED, %s) = %v, want %v", conclusion, got, forgeapi.CheckNeutral)
		}
	}
	if got := h.client.runState("check run", "IN_PROGRESS", ""); got != forgeapi.CheckPending {
		t.Errorf("runState(IN_PROGRESS, no conclusion) = %v, want %v: a run that has not finished reports its status half", got, forgeapi.CheckPending)
	}
}

// TestTheFoldsWorstStateWins holds the fold's ordering, which is the whole of what a
// verdict means: one failing check makes the collection failing however many passed,
// and an unmapped member sits ABOVE pending and passing, because passing has to mean
// every check reported succeeded.
func TestTheFoldsWorstStateWins(t *testing.T) {
	for _, test := range []struct {
		name string
		want forgeapi.CheckState
		add  []forgeapi.CheckState
	}{
		{name: "failing_beats_everything", want: forgeapi.CheckFailing, add: []forgeapi.CheckState{forgeapi.CheckPassing, forgeapi.CheckPending, forgeapi.CheckFailing, forgeapi.CheckNeutral}},
		{name: "unknown_beats_pending_and_passing", want: forgeapi.CheckUnknown, add: []forgeapi.CheckState{forgeapi.CheckPassing, forgeapi.CheckPending, forgeapi.CheckUnknown}},
		{name: "pending_beats_passing", want: forgeapi.CheckPending, add: []forgeapi.CheckState{forgeapi.CheckPassing, forgeapi.CheckPending}},
		{name: "passing_beats_neutral", want: forgeapi.CheckPassing, add: []forgeapi.CheckState{forgeapi.CheckNeutral, forgeapi.CheckPassing}},
		{name: "neutral_alone_is_neutral", want: forgeapi.CheckNeutral, add: []forgeapi.CheckState{forgeapi.CheckNeutral}},
		{name: "nothing_at_all_is_unknown", want: forgeapi.CheckUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			var f fold
			for _, state := range test.add {
				f.add(state, 1)
			}
			if got := f.verdict(); got != test.want {
				t.Errorf("a fold over %v = %v, want %v", test.add, got, test.want)
			}
			if f.total != len(test.add) {
				t.Errorf("a fold over %v = total %d, want %d", test.add, f.total, len(test.add))
			}
		})
	}
}

// TestTheBudgetSignalIsReadFromTheResponseHeaders holds the signal reader to the two
// headers this product sends on both surfaces, and to answering NOTHING where the
// response carried none, which is what keeps the neutral value honest on a connection
// that has sent nothing.
func TestTheBudgetSignalIsReadFromTheResponseHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("X-RateLimit-Remaining", "4990")
	header.Set("X-RateLimit-Reset", "1790000000")
	remaining, reset, ok := signal(header)
	if !ok || remaining != 4990 {
		t.Errorf("signal(the recorded headers) = %d, %v, want 4990 and a window", remaining, ok)
	}
	if reset.IsZero() || reset.Unix() != 1790000000 {
		t.Errorf("signal(the recorded headers) = reset %v, want the epoch second the header names", reset)
	}
	if _, _, ok := signal(http.Header{}); ok {
		t.Error("signal(no headers) reported a figure, want none: a connection that has sent nothing has no budget to report")
	}
}

// TestTheErrorMappingReadsTheHeadersAndTheOperation holds the three cells of this
// family's mapping that turn on something other than the status. A mapper that read the
// status alone would pass every other row of this table, which is why these three are
// named individually.
func TestTheErrorMappingReadsTheHeadersAndTheOperation(t *testing.T) {
	throttle := http.Header{}
	throttle.Set("X-RateLimit-Remaining", "0")
	permission := http.Header{}
	permission.Set("X-RateLimit-Remaining", "4321")
	// downgraded is an instance that answered under the OTHER version it serves
	// rather than the one every request pins, which is the silent-ignore arm.
	downgraded := http.Header{}
	downgraded.Set(headerVersionSelected, "2022-11-28")
	// unserved is the refusal a version outside the instance's set actually
	// carries, measured against the live product: the request is refused before a
	// version is selected, so the echo that rides every other answer, a 404 and a
	// 422 included, is absent from this one.
	unserved := http.Header{}
	unserved.Set(headerRequestID, "AECE:2D9ADE:FD9255A")
	current := http.Header{}
	current.Set(headerVersionSelected, APIVersion)

	for _, test := range []struct {
		header   http.Header
		name     string
		op       string
		wantCode string
		status   int
		wantKind forgeapi.ErrorKind
		pinned   bool
	}{
		{
			name: "a_forbidden_with_no_budget_left_is_a_throttle", op: "ListPRs",
			status: http.StatusForbidden, header: throttle,
			wantKind: forgeapi.KindRateLimited, wantCode: "",
		},
		{
			name: "a_forbidden_with_budget_left_is_a_permission", op: "ListPRs",
			status: http.StatusForbidden, header: permission,
			wantKind: forgeapi.KindForbidden, wantCode: "",
		},
		{
			name: "a_forbidden_on_the_merge_names_the_merge_permission", op: opMergePR,
			status: http.StatusForbidden, header: permission,
			wantKind: forgeapi.KindForbidden, wantCode: forgeapi.CodeNoMergePermission,
		},
		{
			name: "a_conflict_on_the_merge_that_is_refused_names_no_cause", op: opMergePR,
			status: http.StatusConflict, header: nil,
			wantKind: forgeapi.KindConflict, wantCode: "",
		},
		{
			name: "a_bad_request_on_the_merge_names_no_cause", op: opMergePR,
			status: http.StatusBadRequest, header: nil,
			wantKind: forgeapi.KindNotMergeable, wantCode: forgeapi.CodeNotMergeable,
		},
		{
			name: "a_conflict_elsewhere_is_no_stale_head", op: "CreatePR",
			status: http.StatusConflict, header: nil,
			wantKind: forgeapi.KindConflict, wantCode: "",
		},
		{
			name: "a_bad_request_under_another_version_is_the_pin_unserved", op: "ListIssues",
			status: http.StatusBadRequest, header: downgraded, pinned: true,
			wantKind: forgeapi.KindUpstream, wantCode: forgeapi.CodeAPIVersionRetired,
		},
		{
			name: "a_bad_request_that_named_no_version_is_the_pin_refused", op: "ListIssues",
			status: http.StatusBadRequest, header: unserved, pinned: true,
			wantKind: forgeapi.KindUpstream, wantCode: forgeapi.CodeAPIVersionRetired,
		},
		{
			name: "a_bad_request_under_the_pinned_version_is_an_ordinary_refusal", op: "ListIssues",
			status: http.StatusBadRequest, header: current, pinned: true,
			wantKind: forgeapi.KindUpstream, wantCode: "",
		},
		// The connection whose instance serves no such version sends no pin, so its
		// answers name the instance's own default on every response including an
		// ordinary refusal. Reading the echo there is what reported every 400 on such
		// a connection as the pin's own.
		{
			name: "a_bad_request_where_no_pin_rode_the_request_is_not_the_versions_fault", op: "ListIssues",
			status: http.StatusBadRequest, header: downgraded,
			wantKind: forgeapi.KindUpstream, wantCode: "",
		},
		{
			name: "a_bad_request_with_no_echo_and_no_pin_is_not_the_versions_fault", op: "ListIssues",
			status: http.StatusBadRequest, header: unserved,
			wantKind: forgeapi.KindUpstream, wantCode: "",
		},
		{
			name: "a_not_found_is_the_repository_or_the_pull_request", op: "ReadPR",
			status: http.StatusNotFound, header: nil,
			wantKind: forgeapi.KindNotFound, wantCode: forgeapi.CodeRepoOrPRNotVisible,
		},
		{
			name: "a_method_not_allowed_on_the_merge_is_unmergeable", op: opMergePR,
			status: http.StatusMethodNotAllowed, header: nil,
			wantKind: forgeapi.KindNotMergeable, wantCode: forgeapi.CodeNotMergeable,
		},
		{
			name: "an_unprocessable_merge_is_a_branch_that_cannot_merge", op: opMergePR,
			status: http.StatusUnprocessableEntity, header: nil,
			wantKind: forgeapi.KindNotMergeable, wantCode: forgeapi.CodeBranchCannotMerge,
		},
		{
			name: "an_unprocessable_creation_is_a_validation", op: "CreateIssue",
			status: http.StatusUnprocessableEntity, header: nil,
			wantKind: forgeapi.KindUpstream, wantCode: forgeapi.CodeValidation,
		},
		{
			name: "a_gone_issues_list_is_the_issues_feature_switched_off", op: opListIssues,
			status: http.StatusGone, header: nil,
			wantKind: forgeapi.KindNotFound, wantCode: forgeapi.CodeCapabilityUnsupported,
		},
		{
			name: "a_gone_elsewhere_names_no_cause", op: "ReadPR",
			status: http.StatusGone, header: nil,
			wantKind: forgeapi.KindNotFound, wantCode: "",
		},
		{
			name: "too_many_requests_is_the_throttle_it_says_it_is", op: "ListPRs",
			status: http.StatusTooManyRequests, header: nil,
			wantKind: forgeapi.KindRateLimited, wantCode: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := test.header
			if header == nil {
				header = http.Header{}
			}
			kind, code := mapping(test.op, test.status, header, test.pinned)
			if kind != test.wantKind || code != test.wantCode {
				t.Errorf("mapping(%q, %d, %v, pinned %t) = %v, %q, want %v, %q",
					test.op, test.status, header, test.pinned, kind, code, test.wantKind, test.wantCode)
			}
		})
	}
}

// TestAListingRowAnswersNoMergeStrategiesAndTheFullRecordDoes is the measured departure
// this product's minimal repository representation carries, held on both recordings at
// once: the same normalizer answers an empty set from a listing row and the record's own
// flags from the full read, and the evidence per field says which.
func TestAListingRowAnswersNoMergeStrategiesAndTheFullRecordDoes(t *testing.T) {
	recorded := readCaptures(t)
	var listing, full restRepo
	recorded.row(t, "rest_repo_listing_row", &listing)
	recorded.row(t, "rest_repo", &full)

	fromListing := normalizeAffordances(&listing)
	if len(fromListing.MergeStrategies) != 0 {
		t.Errorf("normalizeAffordances(a listing row).MergeStrategies = %v, want none: this product's listing row carries no strategy flag at all",
			fromListing.MergeStrategies)
	}
	fromRecord := normalizeAffordances(&full)
	// The order is the record's own flag order rather than alphabetical, and the set
	// is what the recorded repository allows: a squash and a rebase, with the merge
	// commit refused, so a normalizer answering a fixed list would pass on a
	// repository that refuses one of them.
	if want := []string{"squash", "rebase"}; !slices.Equal(fromRecord.MergeStrategies, want) {
		t.Errorf("normalizeAffordances(the full record).MergeStrategies = %v, want %v: the recorded record allows a squash and a rebase and refuses a merge commit",
			fromRecord.MergeStrategies, want)
	}
	for _, got := range []forgeapi.RepoAffordances{fromListing, fromRecord} {
		if got.MergeTrain != forgeapi.SupportUnknown {
			t.Errorf("MergeTrain = %v, want %v: a merge queue is a ruleset property here, so no repository record answers it", got.MergeTrain, forgeapi.SupportUnknown)
		}
		if got.HasIssues != forgeapi.SupportYes || got.CanPush != forgeapi.SupportYes {
			t.Errorf("HasIssues = %v and CanPush = %v, want both yes: the recorded records carry both keys", got.HasIssues, got.CanPush)
		}
		if got.DefaultBranch != "main" {
			t.Errorf("DefaultBranch = %q, want %q", got.DefaultBranch, "main")
		}
		if ev := got.Ev[forgeapi.CapMergeTrain]; ev.Source != forgeapi.EvidenceDefault {
			t.Errorf("the merge-train evidence source = %q, want %q: the value is a default rather than something the record answered", ev.Source, forgeapi.EvidenceDefault)
		}
	}
	if ev := fromRecord.Ev[forgeapi.CapCanPush]; ev.Source != forgeapi.EvidenceResponseBody {
		t.Errorf("the push evidence source on the full record = %q, want %q", ev.Source, forgeapi.EvidenceResponseBody)
	}
}

// TestADraftReleaseCarriesNoPublicationInstant holds the one release field whose wire
// form is nullable: a draft is a release with no publication, so the instant is the zero
// time rather than a date nothing published.
func TestADraftReleaseCarriesNoPublicationInstant(t *testing.T) {
	recorded := readCaptures(t)
	var published, draft restRelease
	recorded.row(t, "rest_release", &published)
	recorded.row(t, "rest_release_draft", &draft)
	if got := normalizeRelease(&published); got.PublishedAt.IsZero() || got.Draft {
		t.Errorf("normalizeRelease(the published release) = %v, draft %v, want an instant and no draft", got.PublishedAt, got.Draft)
	}
	if got := normalizeRelease(&draft); !got.PublishedAt.IsZero() || !got.Draft || !got.Prerelease {
		t.Errorf("normalizeRelease(the recorded draft) = %v, draft %v, prerelease %v, want the zero instant and both flags",
			got.PublishedAt, got.Draft, got.Prerelease)
	}
}

// TestTheRESTArmAnswersNoCheckStateAtAll holds what the mutation and degraded arm
// cannot supply, which is a property of the ROUTE rather than of the repository: the
// recorded pull request carried two green check runs on the document arm and no check
// or count key at all among the forty-six this route answers.
func TestTheRESTArmAnswersNoCheckStateAtAll(t *testing.T) {
	var record restPull
	readCaptures(t).row(t, "rest_pull", &record)
	h := newHarness(t, nil)
	got := h.client.normalizeRESTPull(&record, testRef())
	if got.Action.Checks != forgeapi.CheckUnknown {
		t.Errorf("normalizeRESTPull(the recorded bytes).Action.Checks = %v, want %v", got.Action.Checks, forgeapi.CheckUnknown)
	}
	if got.Action.ChecksTotal != 0 {
		t.Errorf("normalizeRESTPull(the recorded bytes).Action.ChecksTotal = %d, want 0", got.Action.ChecksTotal)
	}
	if got.Action.QueueState != forgeapi.QueueNone || got.Action.QueuePosition != forgeapi.QueuePositionUnknown {
		t.Errorf("normalizeRESTPull(the recorded bytes) = queue %v at %d, want %v at %d: this route carries no merge-queue key",
			got.Action.QueueState, got.Action.QueuePosition, forgeapi.QueueNone, forgeapi.QueuePositionUnknown)
	}
	if got.Action.MergeBlocked != forgeapi.MergeBlockNone {
		t.Errorf("normalizeRESTPull(the recorded bytes).Action.MergeBlocked = %v, want %v: the recorded close answered a clean merge state",
			got.Action.MergeBlocked, forgeapi.MergeBlockNone)
	}
	if got.Action.AutoMergeArmed != forgeapi.SupportNo {
		t.Errorf("normalizeRESTPull(the recorded bytes).Action.AutoMergeArmed = %v, want %v: the recorded answer's auto-merge key is null, which is the disarmed value",
			got.Action.AutoMergeArmed, forgeapi.SupportNo)
	}
}

// TestAMutationsUnmappedMergeStateIsCountedAndNamed holds the totality table's
// unknown arm on the path that discards what the table answers: a mutation's own
// answer names no block reason whatever merge state it carries, and a member the table
// does not carry is still counted and named there, so an enumeration that grew
// upstream is as visible on a mutation as on a read.
func TestAMutationsUnmappedMergeStateIsCountedAndNamed(t *testing.T) {
	var record map[string]any
	readCaptures(t).row(t, "rest_pull", &record)
	if record["mergeable_state"] != "clean" {
		t.Fatalf("Setup: the recorded pull request carries mergeable_state %v, want the clean member to replace", record["mergeable_state"])
	}
	record["mergeable_state"] = "teleported"
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Setup: re-encoding the recorded pull request: %v", err)
	}
	h := newHarness(t, map[string]string{"POST /api/v3/repos/example/example/pulls": string(body)})
	got, err := h.client.CreatePR(t.Context(), testRef(), forgeapi.NewPullRequest{Title: "t"})
	if err != nil {
		t.Fatalf("CreatePR answering mergeable_state %q = %v, want the pull request", "teleported", err)
	}
	if got.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
		t.Errorf("CreatePR answering mergeable_state %q: Action.MergeBlocked = %v, want %v", "teleported", got.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
	}
	if n := h.spy.times("UnknownEnumValue:mergeable state (rest)"); n != 1 {
		t.Errorf("CreatePR answering mergeable_state %q fired the REST merge-state counter %d time(s), want 1", "teleported", n)
	}
	if !h.logged("teleported") {
		t.Errorf("CreatePR answering mergeable_state %q logged no line naming the value", "teleported")
	}
}

// TestASelectorThatIsNotAnOwnerAndNamePairIsRefused holds the one shape this product's
// documents cannot take, before any request: their two variables are an owner and a
// name, so a selector carrying neither is refused rather than interpolated.
func TestASelectorThatIsNotAnOwnerAndNamePairIsRefused(t *testing.T) {
	for _, selector := range []string{"example", ""} {
		if _, _, err := ownerName(forgeapi.RepoRef{Selector: selector}); err == nil {
			t.Errorf("ownerName(%q) = nil error, want the invalid-reference refusal", selector)
		}
	}
	owner, name, err := ownerName(testRef())
	if err != nil || owner != testOwner || name != testName {
		t.Errorf("ownerName(%q) = %q, %q, %v, want %q, %q and no error", testSelector, owner, name, err, testOwner, testName)
	}
}
