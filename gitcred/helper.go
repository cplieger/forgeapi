// Package gitcred answers git's credential-helper protocol from the records in a
// [creds.Store], so a git operation against a connected forge authenticates with
// that connection's token.
//
// It is the protocol and the origin matching alone. Registering the helper in
// git's configuration, the command line git runs it with, and where the store
// lives are the consumer's: its helper subcommand opens the store and hands
// [Helper.Serve] git's operation argument and its standard streams.
//
// The helper never refreshes a token. It is a short-lived process reading a store
// the consumer's server rotates under a compare-and-swap, so a helper that rotated
// too would race that server and could spend a pair the server then overwrites. A
// token it finds expired is declined with one line saying why.
package gitcred

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/creds"
)

// The decline reasons [forgeapi.Counters.HelperDecline] reports, a closed set.
const (
	reasonUnownedOrigin     = "unowned_origin"
	reasonExpired           = "expired"
	reasonReconnectRequired = "reconnect_required"
	reasonSpent             = "spent"
	reasonNoAccount         = "no_account"
	reasonStoreUnreadable   = "store_unreadable"
)

// The usernames GitHub and GitLab are answered with. GitHub takes any username
// beside a token; GitLab documents oauth2 for an OAuth token in a git URL.
const (
	usernameGitHub = "x-access-token"
	usernameGitLab = "oauth2"
)

var (
	errAnswerUnwritten = errors.New("gitcred: the credential could not be written to git")
	errDiagUnwritten   = errors.New("gitcred: the decline could not be written to the diagnostic stream")
)

// Helper answers git's credential-helper protocol for the connections whose
// records a store holds, and is safe for concurrent use. A get answers the
// connection whose web base URL has the remote's scheme, host and effective
// port, the first by key where several do, with x-access-token as the username
// on GitHub, oauth2 on GitLab and the record's account on Gitea and Forgejo.
// One it cannot answer writes nothing for git, so later helpers and prompting
// still work, is counted through [forgeapi.Counters.HelperDecline], and writes
// one line to diag unless no connection owns the origin.
type Helper struct {
	store    creds.Store
	logger   *slog.Logger
	counters forgeapi.Counters
}

// New builds the helper over store, reading the logger and the counters from
// opts. It reads nothing until [Helper.Serve].
func New(store creds.Store, opts ...forgeapi.Option) *Helper {
	set := forgeapi.Resolve(opts...)
	logger := set.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Helper{store: store, logger: logger, counters: set.Counters}
}

// Serve answers one git invocation: action is git's operation argument, in is
// the attribute block git writes, out is what git reads back, and diag is the
// one line a user sees when a credential is declined. An expired token is
// declined, never renewed here, and so is a record carrying a usability mark the
// server's refresh wrote, with the mark as the reason. A store is ignored; an erase keeps the
// connection and is counted through [forgeapi.Counters.HelperErase]; any other
// action is ignored, as git's protocol asks. A decline is not an error: Serve
// fails only where in exceeds its bound or cannot be read, or where out or diag
// cannot be written.
func (h *Helper) Serve(ctx context.Context, action string, in io.Reader, out, diag io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch action {
	case "get":
		attrs, err := readAttributes(in)
		if err != nil {
			return err
		}
		return h.get(ctx, attrs, out, diag)
	case "erase":
		attrs, err := readAttributes(in)
		if err != nil {
			return err
		}
		h.erase(ctx, attrs)
	}
	return nil
}

func (h *Helper) get(ctx context.Context, attrs attributes, out, diag io.Writer) error {
	rec, found, err := h.owner(attrs)
	switch {
	case err != nil:
		h.logger.WarnContext(ctx, "forgeapi git credential helper cannot read the store", "error", err)
		return h.decline(reasonStoreUnreadable, "", diag)
	case !found:
		return h.decline(reasonUnownedOrigin, "", nil)
	}
	username, reason := answer(&rec, time.Now())
	if reason != "" {
		instance := hostOf(rec.WebBaseURL)
		h.logger.WarnContext(ctx, "forgeapi git credential declined",
			"family", rec.Family.String(), "instance", instance, "reason", reason)
		return h.decline(reason, instance, diag)
	}
	if _, err := fmt.Fprintf(out, "username=%s\npassword=%s\n", username, rec.Token); err != nil {
		return errAnswerUnwritten
	}
	return nil
}

func (h *Helper) erase(ctx context.Context, attrs attributes) {
	rec, found, err := h.owner(attrs)
	switch {
	case err != nil:
		h.logger.WarnContext(ctx, "forgeapi git credential helper cannot read the store", "error", err)
		return
	case !found:
		return
	}
	if h.counters.HelperErase != nil {
		h.counters.HelperErase(rec.Family)
	}
	h.logger.InfoContext(ctx, "forgeapi git credential erase kept the connection",
		"family", rec.Family.String(), "instance", hostOf(rec.WebBaseURL))
}

// owner answers the record whose web base URL has the remote's origin.
func (h *Helper) owner(attrs attributes) (creds.Record, bool, error) {
	remote, ok := remoteOrigin(attrs["protocol"], attrs["host"])
	if !ok {
		return creds.Record{}, false, nil
	}
	keys, err := h.store.Keys()
	if err != nil {
		return creds.Record{}, false, err
	}
	slices.Sort(keys)
	for _, key := range keys {
		rec, found, err := h.store.Load(key)
		if err != nil {
			return creds.Record{}, false, err
		}
		if base, ok := baseOrigin(rec.WebBaseURL); found && ok && base.matches(remote) {
			return rec, true, nil
		}
	}
	return creds.Record{}, false, nil
}

// decline counts one decline and writes its line to diag, where diag is not nil.
func (h *Helper) decline(reason, instance string, diag io.Writer) error {
	if h.counters.HelperDecline != nil {
		h.counters.HelperDecline(reason)
	}
	if diag == nil {
		return nil
	}
	if _, err := io.WriteString(diag, declineLine(reason, instance)+"\n"); err != nil {
		return errDiagUnwritten
	}
	return nil
}

// declineLine is what a user reads when git exits naming no cause, so it says
// what was refused and what fixes it.
func declineLine(reason, instance string) string {
	switch reason {
	case reasonExpired:
		return "git credential helper: the token for " + instance +
			" has expired and was not handed to git; the server holding this connection renews it, so retry once it is running"
	case reasonReconnectRequired:
		return "git credential helper: the stored credential for " + instance +
			" cannot be used or renewed; reconnect it"
	case reasonSpent:
		return "git credential helper: the stored credential for " + instance +
			" was spent by a refresh whose new token this machine lost, so it was not handed to git; reconnect it"
	case reasonNoAccount:
		return "git credential helper: the connection to " + instance +
			" records no account name, which this forge needs as the git username; connect it again"
	}
	return "git credential helper: the credential store cannot be read, so no credential was handed to git"
}

// answer is the username git sends with rec's token, or the reason rec is
// declined.
func answer(rec *creds.Record, now time.Time) (username, reason string) {
	if reason := standing(rec, now); reason != "" {
		return "", reason
	}
	switch rec.Family {
	case forgeapi.FamilyGitHub:
		username = usernameGitHub
	case forgeapi.FamilyGitLab:
		username = usernameGitLab
	case forgeapi.FamilyGitea:
		if rec.Account == "" {
			return "", reasonNoAccount
		}
		username = rec.Account
	default:
		return "", reasonReconnectRequired
	}
	if !carriable(username) || !carriable(rec.Token) {
		return "", reasonReconnectRequired
	}
	return username, ""
}

// standing is the reason rec's token cannot be handed over, empty where it can. A
// usability mark is a verdict the server's refresh reached in its own process, so
// it declines whatever the token's expiry says. An expired token reads as expired
// where the server holding the connection can still renew it, and as
// reconnect-required where nothing can. Only a static token may state no expiry; a
// rotating record stating none is reconnect-required, as the refresh machine reads it.
func standing(rec *creds.Record, now time.Time) string {
	switch {
	case rec.Usability == creds.UsabilitySpent:
		return reasonSpent
	case rec.Usability != creds.UsabilityUnmarked:
		return reasonReconnectRequired
	case rec.Kind != forgeapi.CredKindStaticPAT && rec.Kind != forgeapi.CredKindRotatingOAuth:
		return reasonReconnectRequired
	case rec.Kind == forgeapi.CredKindRotatingOAuth && rec.Expiry.IsZero():
		return reasonReconnectRequired
	case rec.Expiry.IsZero(), now.Before(rec.Expiry):
		return ""
	case renewable(rec, now):
		return reasonExpired
	}
	return reasonReconnectRequired
}

// renewable reports whether the server's refresh can still rotate rec: a family
// with a token endpoint, and a refresh token short of its own expiry. A static
// record holds no refresh token, so it never is.
func renewable(rec *creds.Record, now time.Time) bool {
	refreshes := rec.Family == forgeapi.FamilyGitHub || rec.Family == forgeapi.FamilyGitLab
	return refreshes && rec.RefreshToken != "" &&
		(rec.RefreshExpiry.IsZero() || now.Before(rec.RefreshExpiry))
}

// carriable reports whether a value can travel as one attribute. A newline in a
// token an instance issued would let it write further attributes, a url among
// them, which git reads as the credential's origin.
func carriable(value string) bool {
	return !strings.ContainsAny(value, "\n\x00")
}

// hostOf is a web base URL's host, for a log line and a decline, and empty where
// it does not parse.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}
