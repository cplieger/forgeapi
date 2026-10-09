package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi"
)

func statedMapper(_ string, status int, _ http.Header, bodyError string) (forgeapi.ErrorKind, string) {
	switch {
	case bodyError == "insufficient_scope":
		return forgeapi.KindForbidden, forgeapi.CodeScopeInsufficient
	case status == http.StatusServiceUnavailable:
		return forgeapi.KindTransient, ""
	case status == http.StatusNotFound:
		return forgeapi.KindNotFound, ""
	}
	return forgeapi.KindForbidden, ""
}

func refusingConn(t *testing.T, status int, body string) *Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("Setup: writing the refusal: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	opts := testOptions()
	opts.Mapper = statedMapper
	return openConnWith(t, forgeapi.Connection{WebBaseURL: srv.URL}, opts, forgeapi.WithRetries(0))
}

// RFC 6750's error_description follows the instance's message, except on a scope
// refusal, where it is the text naming what the credential lacks and so leads.
func TestARefusalCarriesTheInstancesOwnWordsForWhatWentWrong(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "a_scope_refusal",
			body: `{"message":"Forbidden","error":"insufficient_scope","error_description":"the token lacks repo","scope":"repo"}`,
			want: "(scope: repo) the token lacks repo",
		},
		{
			name: "another_refusal_with_a_message",
			body: `{"message":"Forbidden","error":"invalid_token","error_description":"the token expired"}`,
			want: "Forbidden",
		},
		{
			name: "another_refusal_with_only_a_description",
			body: `{"error":"invalid_token","error_description":"the token expired"}`,
			want: "the token expired",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := refusingConn(t, http.StatusForbidden, test.body)
			_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("a read refused with %s = %v, want a *forgeapi.Error", test.body, err)
			}
			if fe.Message != test.want {
				t.Errorf("a read refused with %s = message %q, want %q", test.body, fe.Message, test.want)
			}
		})
	}
}

func TestOnlyATransientRefusalIsRetryable(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		retryable bool
	}{
		{name: "a_transient_refusal", status: http.StatusServiceUnavailable, retryable: true},
		{name: "a_permanent_refusal", status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := refusingConn(t, test.status, `{}`)
			_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
			var fe *forgeapi.Error
			if !asError(err, &fe) {
				t.Fatalf("a read refused with %d = %v, want a *forgeapi.Error", test.status, err)
			}
			if fe.Retryable != test.retryable {
				t.Errorf("a read refused with %d = retryable %v, want %v", test.status, fe.Retryable, test.retryable)
			}
		})
	}
}

func TestIsThrottledNamesTheInstancesThrottleAndNotThisLibrarysDeferral(t *testing.T) {
	throttled := refusalOf(t, openTestConn(t, forgeapi.Connection{WebBaseURL: answeringStatus(t, http.StatusTooManyRequests)}))
	if !IsThrottled(throttled) {
		t.Errorf("IsThrottled(%v) = false, want true: the instance answered 429", throttled)
	}
	failed := refusalOf(t, openTestConn(t, forgeapi.Connection{WebBaseURL: answeringStatus(t, http.StatusInternalServerError)}))
	if IsThrottled(failed) {
		t.Errorf("IsThrottled(%v) = true, want false: a 500 is no throttle", failed)
	}
	deferred := refusalOf(t, answeringConn(t, "ListPRs", "1", forgeapi.WithMutationReserve(1)))
	if IsThrottled(deferred) {
		t.Errorf("IsThrottled(%v) = true, want false: a deferral is this library's own choice and sent nothing", deferred)
	}
}

func answeringStatus(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func refusalOf(t *testing.T, c *Conn) error {
	t.Helper()
	ctx := Call(t.Context(), "ListPRs")
	_, err := c.Do(ctx, &Request{Op: "ListPRs", Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("Setup: the read = nil, want a refusal")
	}
	return err
}

// refusingCredential is a credential source that cannot produce a token and says why
// in this library's own error shape, as a refreshing source does once its grant is gone.
type refusingCredential struct{}

func (refusingCredential) Token(context.Context) (string, error) {
	return "", Local(forgeapi.CodeReconnectRequired, "the grant behind this credential was revoked")
}
func (refusingCredential) Kind() forgeapi.CredKind   { return forgeapi.CredKindStaticPAT }
func (refusingCredential) State() forgeapi.CredState { return forgeapi.CredValid }

func TestARefusalFromBeneathTheCallIsCarriedWithTheCallsOperationAndAnID(t *testing.T) {
	c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"}, forgeapi.WithCredentialSource(refusingCredential{}))
	_, err := c.Do(t.Context(), &Request{Op: "Whoami", Method: http.MethodGet, Path: "/user"})
	var fe *forgeapi.Error
	if !asError(err, &fe) {
		t.Fatalf("a read whose credential refused = %v, want a *forgeapi.Error", err)
	}
	if fe.Code != forgeapi.CodeReconnectRequired {
		t.Errorf("the refusal = code %q, want the credential's own %q", fe.Code, forgeapi.CodeReconnectRequired)
	}
	if fe.Op != "Whoami" {
		t.Errorf("the refusal = op %q, want %q", fe.Op, "Whoami")
	}
	if fe.DiagID == "" {
		t.Error("the refusal carries no diagnostic id, want one: the line it names is recorded beside it")
	}
}

// Detection that established nothing must not say the instance lacks it: an
// instance with its evidence switched off may carry it.
func TestACapabilityRefusalSaysWhetherTheInstanceLacksItOrNothingCouldTell(t *testing.T) {
	for _, test := range []struct {
		name    string
		support forgeapi.Support
		absent  bool
	}{
		{name: "read_as_absent", support: forgeapi.SupportNo, absent: true},
		{name: "not_established", support: forgeapi.SupportUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := openTestConn(t, forgeapi.Connection{WebBaseURL: "http://forge.example"})
			fe := c.Refuse(t.Context(), "RerunFailedChecks", forgeapi.CapRerunChecks, test.support, forgeapi.Evidence{})
			if fe.Code != forgeapi.CodeCapabilityUnsupported || fe.Capability != forgeapi.CapRerunChecks {
				t.Errorf("Refuse(%v) = code %q capability %q, want %q and %q", test.support, fe.Code, fe.Capability, forgeapi.CodeCapabilityUnsupported, forgeapi.CapRerunChecks)
			}
			if says := strings.Contains(fe.Message, "carries no "+string(forgeapi.CapRerunChecks)); says != test.absent {
				t.Errorf("Refuse(%v) = message %q, says the instance carries none %v, want %v", test.support, fe.Message, says, test.absent)
			}
		})
	}
}
