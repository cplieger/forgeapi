package creds

import (
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/internal/transport"
	"github.com/cplieger/go/masked/maskedtest"
)

// reviewedTypes are the types the walk does not enter, each with why it need not.
var reviewedTypes = map[reflect.Type]string{
	reflect.TypeFor[time.Time]():         "an instant",
	reflect.TypeFor[slog.Logger]():       "the consumer's logger",
	reflect.TypeFor[forgeapi.Error]():    "a refusal this library words, which quotes no secret (the printer tests pin it)",
	reflect.TypeFor[forgeapi.Settings](): "the resolved options; a credential source an option names is the consumer's",
	reflect.TypeFor[transport.Conn]():    "an endpoint's HTTP client; its credential is a source checked as its own root, and its net/http transport holds the parsed client key and, after a proxied request, the proxy URL with its credentials, the only forms crypto/tls and net/http read them in; what it redacts its answers with is held masked, which the transport's own test checks",
}

// reviewedFields are the text fields the walk accepts, each with why it holds no
// secret.
var reviewedFields = map[maskedtest.Field]string{
	{In: reflect.TypeFor[Record](), Name: "WebBaseURL"}:                 "the instance's origin",
	{In: reflect.TypeFor[Record](), Name: "ClientID"}:                   "the OAuth application's public client ID, sent in clear on every request",
	{In: reflect.TypeFor[Record](), Name: "Scopes"}:                     "the granted scope names",
	{In: reflect.TypeFor[Record](), Name: "Account"}:                    "the account login",
	{In: reflect.TypeFor[DeviceGrant](), Name: "UserCode"}:              "the code the user is shown on purpose, worthless without the device code",
	{In: reflect.TypeFor[DeviceGrant](), Name: "VerificationURI"}:       "the page the user is sent to",
	{In: reflect.TypeFor[DeviceGrant](), Name: "ended"}:                 "the grant's terminal refusal, a *forgeapi.Error this package words",
	{In: reflect.TypeFor[DeviceGrant](), Name: "clientID"}:              "the OAuth application's public client ID",
	{In: reflect.TypeFor[DeviceGrant](), Name: "webBase"}:               "the instance's origin",
	{In: reflect.TypeFor[DeviceGrant](), Name: "grantPath"}:             "the token endpoint's path",
	{In: reflect.TypeFor[AppKey](), Name: "signer"}:                     "the signer a consumer hands NewAppKey, a KMS or HSM handle or a key it holds itself",
	{In: reflect.TypeFor[AppKey](), Name: "issuer"}:                     "the App's client ID or App ID, both public",
	{In: reflect.TypeFor[AppSource](), Name: "instance"}:                "the instance's origin",
	{In: reflect.TypeFor[hold](), Name: "message"}:                      "a refusal this library words",
	{In: reflect.TypeFor[hold](), Name: "pool"}:                         "the rate-limit pool's name",
	{In: reflect.TypeFor[AppGrant](), Name: "Permissions"}:              "permission names and levels",
	{In: reflect.TypeFor[AppGrant](), Name: "Repositories"}:             "repository names",
	{In: reflect.TypeFor[Installation](), Name: "name"}:                 "an account login or a repository name",
	{In: reflect.TypeFor[StoredSource](), Name: "store"}:                "the consumer's store, whose records are checked as Record",
	{In: reflect.TypeFor[StoredSource](), Name: "key"}:                  "the store key the consumer names",
	{In: reflect.TypeFor[custodian](), Name: "backend"}:                 "the store's backend, whose records are checked as Record",
	{In: reflect.TypeFor[custodian](), Name: "custody"}:                 "keyed by store keys; each rotation is checked as its own root",
	{In: reflect.TypeFor[rotation](), Name: "key"}:                      "the store key the consumer names",
	{In: reflect.TypeFor[answer](), Name: "Scope"}:                      "the granted scope names",
	{In: reflect.TypeFor[answer](), Name: "Error"}:                      "the OAuth error member, quoted only when a document defines it",
	{In: reflect.TypeFor[answer](), Name: "UserCode"}:                   "the code the user is shown on purpose",
	{In: reflect.TypeFor[answer](), Name: "VerificationURI"}:            "the page the user is sent to",
	{In: reflect.TypeFor[mintAnswer](), Name: "ExpiresAt"}:              "the token's expiry instant",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "WebBaseURL"}:    "the instance's origin, refused when it carries userinfo",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "APIBaseURL"}:    "the instance's API origin, refused when it carries userinfo",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "CABytes"}:       "the trust anchors' public certificates",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "ClientCert"}:    "the client's public certificate",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "Proxy"}:         "the proxy's URL, refused when it carries userinfo",
	{In: reflect.TypeFor[forgeapi.Connection](), Name: "ProxyUsername"}: "the account the proxy knows the connection as, the password beside it masked",
	{In: reflect.TypeFor[forgeapi.Header](), Name: "Name"}:              "a header's field name",
}

// fileReviewed are the credential-named fields of the credential file, which
// is where a record is stored and so holds its tokens in clear by design.
var fileReviewed = map[maskedtest.Field]string{
	{In: reflect.TypeFor[fileRecord](), Name: "Token"}:        "the credential file is the record's storage, at mode 0600 in a private directory",
	{In: reflect.TypeFor[fileRecord](), Name: "RefreshToken"}: "the credential file is the record's storage, at mode 0600 in a private directory",
}

func TestUnmasked_no_type_holding_a_secret_carries_text_in_clear(t *testing.T) {
	for _, reason := range slices.Concat(slices.Collect(maps.Values(reviewedTypes)), slices.Collect(maps.Values(reviewedFields))) {
		if strings.TrimSpace(reason) == "" {
			t.Fatal("an allow entry has no reason")
		}
	}
	allow := maskedtest.Allow{
		Types:  slices.Collect(maps.Keys(reviewedTypes)),
		Fields: slices.Collect(maps.Keys(reviewedFields)),
	}
	got := maskedtest.Unmasked(allow,
		reflect.TypeFor[Record](), reflect.TypeFor[InstallationToken](), reflect.TypeFor[DeviceGrant](),
		reflect.TypeFor[AppKey](), reflect.TypeFor[AppSource](), reflect.TypeFor[StoredSource](),
		reflect.TypeFor[FileStore](), reflect.TypeFor[rotation](), reflect.TypeFor[answer](),
		reflect.TypeFor[mintAnswer]())
	if len(got) > 0 {
		t.Errorf("Unmasked reported:\n%s", strings.Join(got, "\n"))
	}
}

func TestSecretNamed_no_wire_type_carries_a_credential_unreviewed(t *testing.T) {
	got := maskedtest.SecretNamed(maskedtest.NameAllow{Fields: fileReviewed},
		reflect.TypeFor[answer](), reflect.TypeFor[mintAnswer](), reflect.TypeFor[fileContents](),
		reflect.TypeFor[tokenRequest](), reflect.TypeFor[listed](), reflect.TypeFor[jwtClaims]())
	if len(got) > 0 {
		t.Errorf("SecretNamed reported:\n%s", strings.Join(got, "\n"))
	}
}
