package conformance

import (
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/spec"
)

// githubMergeStates are the mergeability GitHub's REST pull-request record can carry:
// the unsettled answer a mutation usually receives, and the settled members a close
// landing after the check answers.
var githubMergeStates = []struct {
	name      string
	mergeable any
	state     string
}{
	{name: "null_unknown", mergeable: nil, state: "unknown"},
	{name: "true_clean", mergeable: true, state: "clean"},
	{name: "true_blocked", mergeable: true, state: "blocked"},
	{name: "false_dirty", mergeable: false, state: "dirty"},
}

// giteaMergeable is the mergeable flag a Gitea-family pull-request record carries,
// with the verdict a read maps it to: false is also what the record answers while
// the instance holds the pull request queued for its check.
var giteaMergeable = []struct {
	name      string
	mergeable bool
	read      forgeapi.Support
}{
	{name: "mergeable_true", mergeable: true, read: forgeapi.SupportYes},
	{name: "mergeable_false", mergeable: false, read: forgeapi.SupportNo},
}

// pullRequestMutations are the mutations whose own answer is a pull request.
var pullRequestMutations = []string{"PullRequests.CreatePR", "PullRequests.ClosePR", "PullRequests.ReopenPR"}

// withRESTMergeState is a GitHub fixture body with every REST pull-request record it
// holds carrying the mergeable flag and the merge state given.
func withRESTMergeState(t *testing.T, mergeable any, state string) func([]byte) ([]byte, int) {
	t.Helper()
	return func(body []byte) ([]byte, int) {
		return withAltered(t, body, func(n map[string]any) int {
			if _, ok := n["mergeable_state"]; !ok {
				return 0
			}
			n["mergeable"], n["mergeable_state"] = mergeable, state
			return 1
		})
	}
}

// withMergeableFlag is a Gitea-family fixture body with every pull-request record it
// holds carrying the mergeable flag given.
func withMergeableFlag(t *testing.T, mergeable bool) func([]byte) ([]byte, int) {
	t.Helper()
	return func(body []byte) ([]byte, int) {
		return withAltered(t, body, func(n map[string]any) int {
			if _, ok := n["mergeable"].(bool); !ok {
				return 0
			}
			n["mergeable"] = mergeable
			return 1
		})
	}
}

// A GitHub mutation's own answer carries the mergeability its asynchronous check
// held at the instant of the transition, null before the check settles and a
// settled member after it, so neither the mergeable flag nor the block reason it
// answers depends on which: both are unknown on every record, as the creation's,
// the close's and the reopen's rows state them.
func TestAGitHubMutationsOwnAnswerNamesNoMergeabilityWhateverItCarries(t *testing.T) {
	for _, method := range pullRequestMutations {
		for _, st := range githubMergeStates {
			t.Run(strings.TrimPrefix(method, "PullRequests.")+"_"+st.name, func(t *testing.T) {
				what := "mergeable " + st.name
				pr := pullRequestUnder(t, spec.GitHub, method, what, withRESTMergeState(t, st.mergeable, st.state))
				if pr.Action.Mergeable != forgeapi.SupportUnknown {
					t.Errorf("%s on %s answering mergeable %v, mergeable_state %q: Action.Mergeable = %v, want %v",
						method, spec.GitHub, st.mergeable, st.state, pr.Action.Mergeable, forgeapi.SupportUnknown)
				}
				if pr.Action.MergeBlocked != forgeapi.MergeBlockUnknown {
					t.Errorf("%s on %s answering mergeable %v, mergeable_state %q: Action.MergeBlocked = %v, want %v",
						method, spec.GitHub, st.mergeable, st.state, pr.Action.MergeBlocked, forgeapi.MergeBlockUnknown)
				}
			})
		}
	}
}

// A Gitea or Forgejo mutation's own answer carries a mergeable flag that is false
// while the instance holds the pull request queued for its asynchronous check, so a
// false there cannot be told from a conflict and a true says nothing the next push
// keeps: the flag it answers is unknown either way, as each row states it.
func TestAGiteaFamilyMutationsOwnAnswerNamesNoMergeabilityWhateverItCarries(t *testing.T) {
	for _, p := range []spec.Product{spec.Gitea, spec.Forgejo} {
		for _, method := range pullRequestMutations {
			for _, st := range giteaMergeable {
				t.Run(string(p)+"_"+strings.TrimPrefix(method, "PullRequests.")+"_"+st.name, func(t *testing.T) {
					pr := pullRequestUnder(t, p, method, st.name, withMergeableFlag(t, st.mergeable))
					if pr.Action.Mergeable != forgeapi.SupportUnknown {
						t.Errorf("%s on %s answering mergeable %v: Action.Mergeable = %v, want %v",
							method, p, st.mergeable, pr.Action.Mergeable, forgeapi.SupportUnknown)
					}
				})
			}
		}
	}
}

// A Gitea or Forgejo read keeps mapping the mergeable flag it reads onto the
// verdict, on the single read and on the repository's list alike.
func TestAGiteaFamilyReadMapsTheMergeableFlagItReads(t *testing.T) {
	for _, p := range []spec.Product{spec.Gitea, spec.Forgejo} {
		for _, method := range []string{"PullRequests.ReadPR", "PullRequests.ListPRs"} {
			for _, st := range giteaMergeable {
				t.Run(string(p)+"_"+strings.TrimPrefix(method, "PullRequests.")+"_"+st.name, func(t *testing.T) {
					pr := pullRequestUnder(t, p, method, st.name, withMergeableFlag(t, st.mergeable))
					if pr.Action.Mergeable != st.read {
						t.Errorf("%s on %s reading mergeable %v: Action.Mergeable = %v, want %v",
							method, p, st.mergeable, pr.Action.Mergeable, st.read)
					}
				})
			}
		}
	}
}
