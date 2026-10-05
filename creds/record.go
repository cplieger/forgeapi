// Package creds is the credential machinery behind a forgeapi connection: the
// refresh state machine a rotating OAuth credential runs under, the OAuth device
// grant that mints one, and the file-backed store both of them read.
//
// The vocabulary it implements, [forgeapi.CredentialSource], [forgeapi.CredKind]
// and [forgeapi.CredState], is the root package's. Nothing here reads the
// environment and no goroutine or timer runs here: a consumer keeps a credential
// fresh by calling Token, which every operation it issues already does.
package creds

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/cplieger/forgeapi"
)

// Usability is what a refresh learned about a record's stored pair that the
// record's other fields cannot say. Every reader of the record honours it, in
// any process: a marked record is never handed over and never refreshed.
type Usability int

// The Usability members.
const (
	// UsabilityUnmarked is the zero value: nothing is recorded, so the kind and
	// the expiry decide. A connect or a reconnect saves a record unmarked, which
	// is what clears a mark.
	UsabilityUnmarked Usability = iota
	// UsabilitySpent is a pair the instance rotated whose successor was lost, so
	// the stored token is dead upstream while the record still holds it.
	UsabilitySpent
	// UsabilityReconnectRequired is a pair whose refresh the instance answered
	// terminally.
	UsabilityReconnectRequired
)

// String returns the member's spelling: "unmarked", "spent" or
// "reconnect_required". The store's file spells a mark the same way and writes
// no member for an unmarked record, so "unmarked" is never in a file. A value
// outside the members reads, and is spelled, as reconnect_required, because an
// unknown mark must not read as usable.
func (u Usability) String() string {
	switch u {
	case UsabilityUnmarked:
		return "unmarked"
	case UsabilitySpent:
		return "spent"
	}
	return "reconnect_required"
}

var _ fmt.Stringer = Usability(0)

// Record is one connection's stored credential. The consumer saves what a grant
// answers, or a record it builds for a personal access token; the library writes a
// record only to rotate it or to mark it. A zero-valued record has the unknown kind
// and reads as reconnect-required, never as a static token that needs no refresh.
type Record struct { //nolint:govet // fieldalignment: the field order is a record's own reading order, where it is addressed first and what it holds after
	// Family and WebBaseURL are where a refresh is addressed and what the git
	// credential helper matches an origin against.
	Family        forgeapi.Family
	WebBaseURL    string
	Kind          forgeapi.CredKind
	Token         string
	Issued        time.Time // when the grant or the last rotation answered
	Expiry        time.Time // zero where the response stated none
	RefreshToken  string    // empty where the response carried none
	RefreshExpiry time.Time // zero where the response stated none
	ClientID      string    // the OAuth application that minted the grant
	Scopes        []string  // as the response stated them, where it did
	Account       string
	// Usability is the mark a refresh writes when it learns the stored pair can
	// no longer be used, which the kind and the expiry cannot say.
	Usability Usability
}

// same reports whether two records are the same record, comparing times as
// instants. It is the comparison of the rotation's compare-and-swap.
func same(a, b *Record) bool {
	return a.Family == b.Family && a.WebBaseURL == b.WebBaseURL && a.Kind == b.Kind &&
		a.Token == b.Token && a.Issued.Equal(b.Issued) && a.Expiry.Equal(b.Expiry) &&
		a.RefreshToken == b.RefreshToken && a.RefreshExpiry.Equal(b.RefreshExpiry) &&
		a.ClientID == b.ClientID && slices.Equal(a.Scopes, b.Scopes) && a.Account == b.Account &&
		a.Usability == b.Usability
}

// expired reports whether a record's token has an expiry and has reached it.
func expired(rec *Record, now time.Time) bool {
	return !rec.Expiry.IsZero() && !now.Before(rec.Expiry)
}

// fileRecord is a record as the store's file spells it: the family, the kind and
// the mark by their names, so the file stays readable across a reordering nobody
// intends. The file carries no version number; it moves by members, each read so
// that a file written before the member reads as it did then.
type fileRecord struct {
	Issued        time.Time `json:"issued,omitzero"`
	Expiry        time.Time `json:"expiry,omitzero"`
	RefreshExpiry time.Time `json:"refresh_expiry,omitzero"`
	// Usability is absent for an unmarked record, which is how every record
	// written before the member reads. It is held raw, so a present member is
	// told from an absent one whatever value it carries, null included.
	Usability    json.RawMessage `json:"usability,omitempty"`
	Family       string          `json:"family"`
	WebBaseURL   string          `json:"web_base_url"`
	Kind         string          `json:"kind"`
	Token        string          `json:"token"`
	RefreshToken string          `json:"refresh_token,omitempty"`
	ClientID     string          `json:"client_id,omitempty"`
	Account      string          `json:"account,omitempty"`
	Scopes       []string        `json:"scopes,omitempty"`
}

func toFile(rec *Record) fileRecord {
	f := fileRecord{
		Family: rec.Family.String(), WebBaseURL: rec.WebBaseURL, Kind: rec.Kind.String(),
		Token: rec.Token, Issued: rec.Issued, Expiry: rec.Expiry,
		RefreshToken: rec.RefreshToken, RefreshExpiry: rec.RefreshExpiry,
		ClientID: rec.ClientID, Scopes: rec.Scopes, Account: rec.Account,
	}
	if rec.Usability != UsabilityUnmarked {
		f.Usability = strconv.AppendQuote(nil, rec.Usability.String())
	}
	return f
}

// fromFile reads a name this library does not write as the unknown member, which
// for the kind is reconnect-required.
func fromFile(f *fileRecord) Record {
	return Record{
		Family: member(f.Family, forgeapi.FamilyUnknown, forgeapi.FamilyGitea), WebBaseURL: f.WebBaseURL,
		Kind:  member(f.Kind, forgeapi.CredKindUnknown, forgeapi.CredKindRotatingOAuth),
		Token: f.Token, Issued: f.Issued, Expiry: f.Expiry,
		RefreshToken: f.RefreshToken, RefreshExpiry: f.RefreshExpiry,
		ClientID: f.ClientID, Scopes: f.Scopes, Account: f.Account,
		Usability: usabilityOf(f.Usability),
	}
}

// usabilityOf reads the file's mark. An absent member is unmarked; a present one
// holding anything but a spelling this library writes, "unmarked" and null among
// them since an unmarked record omits the member, reads as reconnect-required,
// because the unmarked zero is the member that lets the kind and the expiry decide.
func usabilityOf(raw json.RawMessage) Usability {
	if len(raw) == 0 {
		return UsabilityUnmarked
	}
	var name string
	if err := json.Unmarshal(raw, &name); err == nil && name == UsabilitySpent.String() {
		return UsabilitySpent
	}
	return UsabilityReconnectRequired
}

// member is the enumeration member from first to last whose spelling is name, and
// first, the unknown member, where none is.
func member[T interface {
	~int
	String() string
}](name string, first, last T) T {
	for m := first; m <= last; m++ {
		if m.String() == name {
			return m
		}
	}
	return first
}
