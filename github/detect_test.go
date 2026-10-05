package github

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

// The routes connection setup and the grant read reach, which are the only requests
// the three capability accessors can make.
const (
	metaRoute     = "GET /api/v3/meta"
	userRoute     = "GET /api/v3/user"
	versionsRoute = "GET /api/v3" + versionsPath
)

// servedVersions and unservedVersions are the two answers the version set can give a
// connection: an instance serving the version this family pins, which is what the
// hosted product answers, and one serving only the older version it supersedes, which
// is the appliance case the omit arm exists for.
const (
	servedVersions   = `["` + APIVersion + `","2022-11-28"]`
	unservedVersions = `["2022-11-28"]`
)

// withWitness is the header this product names itself with, which is the family
// evidence and rides every response including a refusal.
func withWitness(in *instance) {
	in.answerHeader(headerRequestID, "EXAMPLE:REQUEST:ID")
}

// TestTheConnectionReadEstablishesTheFamilyFromTheHeader holds connection setup's own
// price and its evidence: TWO requests on a connection that has sent nothing, the
// metadata read that carries the header and the version set the pin turns on, the
// family taken from the header rather than from the body, and the capability answered
// unknown with its reason, because the runner this product gates the re-run on is
// enabled per REPOSITORY and no connection-scope read names it.
func TestTheConnectionReadEstablishesTheFamilyFromTheHeader(t *testing.T) {
	h := newHarness(t, map[string]string{
		metaRoute:     `{"installed_version":"3.20.0"}`,
		versionsRoute: servedVersions,
	})
	withWitness(h.instance)

	caps, err := h.client.ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
	}
	if got := h.instance.count(); got != 2 {
		t.Errorf("ConnectionCaps sent %d request(s), want 2: the metadata read carries the header on a connection that has sent nothing and the version set is what the pin turns on: %v",
			got, h.instance.arrived())
	}
	if got := caps.Caps[forgeapi.CapRerunChecks]; got != forgeapi.SupportUnknown {
		t.Errorf("ConnectionCaps = re-run %v, want %v: this product enables the runner per repository and the read that answers it for one needs admin",
			got, forgeapi.SupportUnknown)
	}
	ev := caps.Ev[forgeapi.CapRerunChecks]
	if ev.Source != forgeapi.EvidenceMetadata {
		t.Errorf("ConnectionCaps = evidence source %q, want %q: the metadata endpoint is what answered", ev.Source, forgeapi.EvidenceMetadata)
	}
	if !strings.Contains(ev.Detail, "3.20.0") {
		t.Errorf("ConnectionCaps = evidence %q, want the appliance version it read named", ev.Detail)
	}
	if !strings.Contains(ev.Detail, "per repository") {
		t.Errorf("ConnectionCaps = evidence %q, want the reason the verdict is unknown: an unknown with no cause is what a consumer cannot render", ev.Detail)
	}
}

// TestTheConnectionReadCostsNothingTwice holds the cached arm of the price: the answer
// is held with the client, so every later call spends nothing whatever it is asked.
func TestTheConnectionReadCostsNothingTwice(t *testing.T) {
	h := newHarness(t, map[string]string{
		metaRoute:     `{"installed_version":"3.20.0"}`,
		versionsRoute: servedVersions,
	})
	withWitness(h.instance)
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("Setup: ConnectionCaps = %v", err)
	}
	before := h.instance.count()
	if _, err := h.client.ConnectionCaps(t.Context()); err != nil {
		t.Fatalf("ConnectionCaps on a warm connection = %v, want the held answer", err)
	}
	if got := h.instance.count() - before; got != 0 {
		t.Errorf("ConnectionCaps on a warm connection sent %d request(s), want 0", got)
	}
}

// TestTheConnectionReadCostsNothingAfterOtherTraffic holds the lower arm of the
// published range, which is what makes it a range at all: the family witness rides
// every response, so a connection that has already read anything spends nothing on that
// half and the one request left is the version set, which no response carries.
func TestTheConnectionReadCostsNothingAfterOtherTraffic(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		userRoute:     recorded.raw(t, "rest_user"),
		versionsRoute: servedVersions,
	})
	withWitness(h.instance)
	if _, err := h.client.Whoami(t.Context()); err != nil {
		t.Fatalf("Setup: Whoami = %v", err)
	}
	before := h.instance.count()
	caps, err := h.client.ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("ConnectionCaps = %v, want the capabilities", err)
	}
	if got := h.instance.count() - before; got != 1 {
		t.Errorf("ConnectionCaps after other traffic sent %d request(s), want 1: the witness already arrived and the version set is the half no response carries: %v",
			got, h.instance.arrived())
	}
	if arrived := h.instance.arrived(); arrived[len(arrived)-1] != versionsRoute {
		t.Errorf("ConnectionCaps after other traffic reached %q, want %q: the metadata read is what the witness makes unnecessary", arrived[len(arrived)-1], versionsRoute)
	}
	if got := caps.Ev[forgeapi.CapRerunChecks].Source; got != forgeapi.EvidenceResponseHeader {
		t.Errorf("ConnectionCaps = evidence source %q, want %q: what established the family was a header on traffic already sent", got, forgeapi.EvidenceResponseHeader)
	}
}

// TestAnInstanceWithNoWitnessIsRefused holds the refusal every route below detection
// depends on: a response carrying no header of this product's own and no prior witness
// cannot establish a family, and every route, enumeration table and error mapping under
// this point is one family's.
func TestAnInstanceWithNoWitnessIsRefused(t *testing.T) {
	h := newHarness(t, map[string]string{metaRoute: `{"installed_version":"3.20.0"}`})
	_, err := h.client.ConnectionCaps(t.Context())
	var fe *forgeapi.Error
	if !asForgeError(err, &fe) || fe.Code != forgeapi.CodeFamilyUndetected {
		t.Fatalf("ConnectionCaps against an instance with no witness = %v, want code %q", err, forgeapi.CodeFamilyUndetected)
	}
	if fe.DiagID == "" {
		t.Error("the refusal carries no diagnostic id, want one: a request was made and identified no family")
	}
}

// TestAMetadataReadTheCredentialCannotMakeStillEstablishesTheFamily holds the decision
// rather than the swallow: the header names the product on a refusal too, so the family
// IS established and what could not be read was a version this scope's verdict does not
// turn on. An error here would make every other operation unreachable on a connection
// whose public reads work.
func TestAMetadataReadTheCredentialCannotMakeStillEstablishesTheFamily(t *testing.T) {
	header := http.Header{}
	header.Set(headerRequestID, "EXAMPLE:REQUEST:ID")
	h := newRefusingHarness(t, http.StatusUnauthorized, header)
	caps, err := h.client.ConnectionCaps(t.Context())
	if err != nil {
		t.Fatalf("ConnectionCaps against a refused metadata read = %v, want the capabilities", err)
	}
	ev := caps.Ev[forgeapi.CapRerunChecks]
	if ev.Source != forgeapi.EvidenceResponseHeader {
		t.Errorf("ConnectionCaps = evidence source %q, want %q", ev.Source, forgeapi.EvidenceResponseHeader)
	}
	if !strings.Contains(ev.Detail, http.StatusText(http.StatusUnauthorized)) {
		t.Errorf("ConnectionCaps = evidence %q, want the status the metadata endpoint answered", ev.Detail)
	}
}

// TestTheGrantReadAnswersFromTheScopeHeader holds the grant accessor's price and its
// evidence. The verdict is yes whatever the grant, measured: one document answered the
// merge state beside a viewer permission of READ, so routing it through an unknown
// would render a disabled control with a reason that is not true. What the header adds
// is what the credential actually holds.
func TestTheGrantReadAnswersFromTheScopeHeader(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{userRoute: recorded.raw(t, "rest_user")})
	h.instance.answerHeaders(userRoute, http.Header{"X-Oauth-Scopes": []string{"repo, workflow"}})

	grant, err := h.client.GrantCaps(t.Context())
	if err != nil {
		t.Fatalf("GrantCaps = %v, want the capabilities", err)
	}
	if got := h.instance.count(); got != 1 {
		t.Errorf("GrantCaps sent %d request(s), want 1", got)
	}
	if got := grant.Caps[forgeapi.CapReadMergeState]; got != forgeapi.SupportYes {
		t.Errorf("GrantCaps = read-merge-state %v, want %v", got, forgeapi.SupportYes)
	}
	ev := grant.Ev[forgeapi.CapReadMergeState]
	if ev.Source != forgeapi.EvidenceResponseHeader || !strings.Contains(ev.Detail, "repo, workflow") {
		t.Errorf("GrantCaps = evidence %q from %q, want the scope pair the header carried", ev.Detail, ev.Source)
	}
	before := h.instance.count()
	if _, err := h.client.GrantCaps(t.Context()); err != nil {
		t.Fatalf("GrantCaps on a warm connection = %v", err)
	}
	if got := h.instance.count() - before; got != 0 {
		t.Errorf("GrantCaps on a warm connection sent %d request(s), want 0", got)
	}
}

// TestACredentialReportingNoScopesIsNamedRatherThanInferredFrom holds the evidence on
// the credential kind this product reports an EMPTY scope set for: a fine-grained token
// is documented to do so, so the detail says what was read rather than treating the
// absence as a grant fact.
func TestACredentialReportingNoScopesIsNamedRatherThanInferredFrom(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{userRoute: recorded.raw(t, "rest_user")})
	grant, err := h.client.GrantCaps(t.Context())
	if err != nil {
		t.Fatalf("GrantCaps = %v, want the capabilities", err)
	}
	if got := grant.Caps[forgeapi.CapReadMergeState]; got != forgeapi.SupportYes {
		t.Errorf("GrantCaps = read-merge-state %v, want %v: the scope set says nothing about a field readable by any credential", got, forgeapi.SupportYes)
	}
	if detail := grant.Ev[forgeapi.CapReadMergeState].Detail; !strings.Contains(detail, "fine-grained") {
		t.Errorf("GrantCaps = evidence %q, want the credential kind an empty scope set names", detail)
	}
}

// TestTheIdentityReadFillsTheGrantAtNoFurtherRequest holds the sharing that prices the
// grant accessor at nothing on a used connection: the scope header the identity read
// already carried is what the grant answers from, so a consumer that read the account
// pays once for both.
func TestTheIdentityReadFillsTheGrantAtNoFurtherRequest(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{userRoute: recorded.raw(t, "rest_user")})
	h.instance.answerHeaders(userRoute, http.Header{"X-Oauth-Scopes": []string{"repo, workflow"}})

	account, err := h.client.Whoami(t.Context())
	if err != nil {
		t.Fatalf("Whoami = %v, want the account", err)
	}
	if want := []string{"repo", "workflow"}; strings.Join(account.Scopes, ",") != strings.Join(want, ",") {
		t.Errorf("Whoami = scopes %v, want %v: this product reports them in a header rather than in the body", account.Scopes, want)
	}
	before := h.instance.count()
	if _, err := h.client.GrantCaps(t.Context()); err != nil {
		t.Fatalf("GrantCaps after the identity read = %v", err)
	}
	if got := h.instance.count() - before; got != 0 {
		t.Errorf("GrantCaps after the identity read sent %d request(s), want 0", got)
	}
}

// TestTheAffordanceReadIsNotCached holds the one accessor that reads every time, which
// is a boundary decision rather than an oversight: a repository cache would be a fourth
// kind of per-connection state, and the closed list has three.
func TestTheAffordanceReadIsNotCached(t *testing.T) {
	recorded := readCaptures(t)
	h := newHarness(t, map[string]string{
		"GET /api/v3/repos/example/example": recorded.raw(t, "rest_repo"),
	})
	for i := range 2 {
		if _, err := h.client.RepoAffordances(t.Context(), testRef()); err != nil {
			t.Fatalf("RepoAffordances call %d = %v, want the affordances", i+1, err)
		}
	}
	if got := h.instance.count(); got != 2 {
		t.Errorf("two affordance reads sent %d request(s), want 2: this client holds no repository cache", got)
	}
}
