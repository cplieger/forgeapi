package gitlab

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// TestTheFamilyIsEstablishedFromAMarkerRatherThanFromAVersionKey holds what detection
// has to prove before this package serves a connection at all.
//
// Every route, every enumeration table and every error mapping below detection is one
// family's, so a server identified on nothing but the presence of a version string is a
// server whose answers are then read as this family's. Any service can answer a metadata
// route with a version key, and one of the cases below is a body this product's own
// sibling family sends.
//
// What this product DOES name itself with is the header, and the name is what makes it a
// witness: it is called after the product. Measured on gitlab.com it rides every
// response including a 401, which is the case that lets a connection whose credential
// cannot read the metadata endpoint still be served.
func TestTheFamilyIsEstablishedFromAMarkerRatherThanFromAVersionKey(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     string
		header   string
		status   int
		detected bool
		support  forgeapi.Support
	}{
		{
			name:     "the_header_names_the_product",
			body:     metadataBody,
			header:   headerMeta,
			detected: true,
			support:  forgeapi.SupportYes,
		},
		{
			name:     "the_header_rides_a_refusal_too_so_the_family_is_established_and_the_capability_is_not",
			body:     `{"message":"401 Unauthorized"}`,
			header:   headerMeta,
			status:   http.StatusUnauthorized,
			detected: true,
			support:  forgeapi.SupportUnknown,
		},
		{
			name:     "a_body_carrying_a_version_and_no_header_establishes_nothing",
			body:     metadataBody,
			detected: false,
		},
		{
			// The case this assertion exists for: a body the sibling family
			// sends, whose own version key would satisfy any check that read the
			// body rather than the mark.
			name:     "a_foreign_family_s_own_version_body_is_no_witness",
			body:     `{"version":"1.27.0+dev-954-g1f3981a301"}`,
			detected: false,
		},
		{
			name:     "a_body_that_is_not_json_at_all_is_no_witness",
			body:     `<html><title>Some other service</title></html>`,
			detected: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{metadataRoute: test.body})
			if test.header != "" {
				h.instance.answerHeader(test.header, `{"correlation_id":"example","version":"1"}`)
			}
			if test.status != 0 {
				h.instance.status(metadataRoute, test.status)
			}
			caps, err := h.client.ConnectionCaps(t.Context())
			if !test.detected {
				var fe *forgeapi.Error
				if !asForgeError(err, &fe) {
					t.Fatalf("ConnectionCaps = %+v, %v, want a refusal: nothing here identifies the family", caps, err)
				}
				if fe.Code != forgeapi.CodeFamilyUndetected {
					t.Errorf("ConnectionCaps = code %q, want %q", fe.Code, forgeapi.CodeFamilyUndetected)
				}
				if fe.Status == 0 {
					t.Error("ConnectionCaps = status 0, want the real one: this refusal made a request and identified no family, which is what separates it from the refusals reached before anything was sent")
				}
				// A refusal that cached capability state would answer the second
				// caller as if the family had been established.
				if _, again := h.client.ConnectionCaps(t.Context()); again == nil {
					t.Error("a second ConnectionCaps = nil, want the same refusal: nothing is cached for a family that was never established")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConnectionCaps = %v, want the capabilities: this instance carries this product's own mark", err)
			}
			if got := caps.Caps[forgeapi.CapRerunChecks]; got != test.support {
				t.Errorf("ConnectionCaps = re-run %v, want %v", got, test.support)
			}
			if caps.Ev[forgeapi.CapRerunChecks].Source != forgeapi.EvidenceMetadata {
				t.Errorf("ConnectionCaps = evidence source %q, want %q: an unknown renders a disabled control with its REASON, so the source is what a consumer shows",
					caps.Ev[forgeapi.CapRerunChecks].Source, forgeapi.EvidenceMetadata)
			}
			if caps.Ev[forgeapi.CapRerunChecks].Detail == "" {
				t.Error("ConnectionCaps = empty evidence detail, want the version or the status: an unknown with no cause is an unexplained disabled control")
			}
		})
	}
}

// TestACapabilityUnknownRefusesTheOperationRatherThanSendingIt holds the dedicated
// refusal, which is the arm a connection whose credential cannot read the
// metadata endpoint reaches: the family is established, the capability is not, and the
// operation gated on it is refused before any request rather than sent to fail.
func TestACapabilityUnknownRefusesTheOperationRatherThanSendingIt(t *testing.T) {
	h := newHarness(t, map[string]string{
		metadataRoute:  `{"message":"401 Unauthorized"}`,
		pipelinesRoute: pipelinesBody,
		retryRoute:     `{}`,
	})
	h.instance.answerHeader(headerMeta, `{"correlation_id":"example","version":"1"}`)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities with the re-run unknown", err)
	}
	before := h.instance.count()
	err := h.client.RerunFailedChecks(t.Context(), testRef(), testPR(), testHeadSHA)
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) {
		t.Fatalf("RerunFailedChecks on an unresolved capability = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeCapabilityUnsupported {
		t.Errorf("RerunFailedChecks = code %q, want %q", fe.Code, forgeapi.CodeCapabilityUnsupported)
	}
	if fe.Capability != forgeapi.CapRerunChecks {
		t.Errorf("RerunFailedChecks = capability %q, want %q", fe.Capability, forgeapi.CapRerunChecks)
	}
	if fe.Evidence.Source == forgeapi.EvidenceUnknown {
		t.Error("RerunFailedChecks = unknown evidence source, want the source detection held for the verdict")
	}
	if fe.Status != 0 {
		t.Errorf("RerunFailedChecks = status %d, want 0: no request was sent", fe.Status)
	}
	if fe.DiagID == "" {
		t.Error("RerunFailedChecks = empty diagnostic id, want one: this refusal names its operation and its family")
	}
	if spent := h.instance.count() - before; spent != 0 {
		t.Errorf("RerunFailedChecks sent %d request(s), want 0: detection refuses the call before any request", spent)
	}
}

// TestTheGrantIsReadFromTheDocumentEveryReadAlreadySent holds the grant accessor's price
// and its three answers.
//
// It names no repository, so it has no project path and no merge-request number to send
// a document with, and the permissions it reports ride every document a read of this
// product already sends. That is why it costs zero requests, and it is also why a cold
// connection has to answer from this product's own default rather than from unknown: the
// merge state is selected by both documents and readable by any credential that can read
// the merge request, so an unknown there would disable a control that works on every
// instance of it.
func TestTheGrantIsReadFromTheDocumentEveryReadAlreadySent(t *testing.T) {
	t.Run("cold_it_answers_this_product_s_own_default_at_no_request", func(t *testing.T) {
		h := newHarness(t, nil)
		caps, err := h.client.GrantCaps(t.Context())
		if err != nil {
			t.Fatalf("GrantCaps = %v, want the capabilities", err)
		}
		if h.instance.count() != 0 {
			t.Errorf("GrantCaps sent %d request(s), want 0: it names no repository, so there is no document for it to send", h.instance.count())
		}
		if got := caps.Caps[forgeapi.CapReadMergeState]; got != forgeapi.SupportYes {
			t.Errorf("GrantCaps = merge state %v, want %v: both documents select the field, so an unknown here disables a control that works everywhere",
				got, forgeapi.SupportYes)
		}
		if caps.Ev[forgeapi.CapReadMergeState].Source != forgeapi.EvidenceDefault {
			t.Errorf("GrantCaps = evidence source %q on a connection that has sent no document, want %q",
				caps.Ev[forgeapi.CapReadMergeState].Source, forgeapi.EvidenceDefault)
		}
	})
	for _, test := range []struct {
		name    string
		body    string
		want    forgeapi.Support
		pushing string
	}{
		{
			name:    "a_credential_the_instance_reports_as_able_to_push_can_ask_for_a_recalculation",
			body:    docRead,
			want:    forgeapi.SupportYes,
			pushing: "pushCode true",
		},
		{
			name: "one_it_reports_otherwise_answers_unknown_rather_than_no",
			body: `{"data": {"queryComplexity": {"score": 46, "limit": 200},
  "project": {"fullPath": "example/group/example",
    "userPermissions": {"pushCode": false, "readMergeRequest": true},
    "mergeRequest": ` + docNode + `}}}`,
			want:    forgeapi.SupportUnknown,
			pushing: "pushCode false",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, map[string]string{documentRoute: test.body})
			if _, err := h.client.ReadPR(t.Context(), testRef(), testPR()); err != nil {
				t.Fatalf("ReadPR = %v, want the merge request", err)
			}
			before := h.instance.count()
			caps, err := h.client.GrantCaps(t.Context())
			if err != nil {
				t.Fatalf("GrantCaps = %v, want the capabilities", err)
			}
			if spent := h.instance.count() - before; spent != 0 {
				t.Errorf("GrantCaps sent %d request(s) after a document had run, want 0", spent)
			}
			if got := caps.Caps[forgeapi.CapReadMergeState]; got != test.want {
				t.Errorf("GrantCaps = merge state %v, want %v: this product's recalculation is refusable below a project role, so the freshness is a grant fact",
					got, test.want)
			}
			ev := caps.Ev[forgeapi.CapReadMergeState]
			if ev.Source != forgeapi.EvidenceResponseBody {
				t.Errorf("GrantCaps = evidence source %q after a document had run, want %q", ev.Source, forgeapi.EvidenceResponseBody)
			}
			if ev.Detail == "" || !strings.Contains(ev.Detail, test.pushing) {
				t.Errorf("GrantCaps = evidence detail %q, want it to name %q: the pair is what the verdict rests on", ev.Detail, test.pushing)
			}
		})
	}
}

// TestTheGovernorPublishesTheSignalThisProductSends holds what the budget accessor
// answers after one ordinary read, which is what makes the figure cost-free: it rides
// the response the operation already received.
func TestTheGovernorPublishesTheSignalThisProductSends(t *testing.T) {
	h := newHarness(t, map[string]string{projectsListRoute: "[" + projectBody + "]"})
	h.instance.answerHeader("RateLimit-Remaining", "4990")
	h.instance.answerHeader("RateLimit-Reset", "1790000000")
	before := h.client.BudgetState()
	if before.Remaining != forgeapi.BudgetRemainingUnknown {
		t.Errorf("BudgetState before any call = remaining %d, want %d: a connection that has sent nothing has no figure",
			before.Remaining, forgeapi.BudgetRemainingUnknown)
	}
	if _, err := h.client.ListRepos(t.Context()); err != nil {
		t.Fatalf("ListRepos = %v", err)
	}
	got := h.client.BudgetState()
	if got.Remaining != 4990 {
		t.Errorf("BudgetState = remaining %d, want 4990 from the header the response carried", got.Remaining)
	}
	if got.Reset.IsZero() {
		t.Error("BudgetState = a zero reset, want the instant the window renews")
	}
	if got.LastCost != 1 {
		t.Errorf("BudgetState = last cost %d, want 1: a REST request costs one on this product, where a document costs its complexity", got.LastCost)
	}
	if got.RotationCursor != "" {
		t.Errorf("BudgetState = rotation cursor %q, want empty: nothing rotates on this family, because the folded verdict is a scalar the list document already selected",
			got.RotationCursor)
	}
}
