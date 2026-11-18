package families

import (
	"context"
	"errors"

	"github.com/cplieger/forgeapi/v3"
	"github.com/cplieger/forgeapi/v3/gitea"
	"github.com/cplieger/forgeapi/v3/github"
	"github.com/cplieger/forgeapi/v3/gitlab"
	"github.com/cplieger/forgeapi/v3/internal/transport"
)

// Open asks GitLab's connection read, then GitHub's, then the Gitea family's, and answers the
// first family it establishes with its client, capabilities held, at that read plus one request
// per family asked first (at most 2, 3 and 5), on no budget the caller holds. Every failure
// answers [forgeapi.FamilyUnknown]: no family established is [forgeapi.CodeFamilyUndetected] with
// the last read's status and figures; a context ending, a constructor's refusal, a refused attempt
// and a governor deferral return unchanged and ask no later family. Open closes all but its answer.
//
//nolint:gocritic // hugeParam: the connection record is the published signature's own parameter
func Open(ctx context.Context, conn forgeapi.Connection, opts ...forgeapi.Option) (forgeapi.Core, forgeapi.Family, error) {
	var refused error
	for _, candidate := range detection {
		client, err := candidate.open(&conn, opts)
		if err != nil {
			return nil, forgeapi.FamilyUnknown, err
		}
		_, err = client.ConnectionCaps(ctx)
		// The caller's context, not the error: a read that outlived its own operation
		// deadline answers the same sentinel and is that family's absence.
		if ctxErr := ctx.Err(); ctxErr != nil {
			client.Close()
			return nil, forgeapi.FamilyUnknown, ctxErr
		}
		if err == nil {
			return client, candidate.family, nil
		}
		client.Close()
		if transport.IsRefused(err) || transport.IsDeferred(err) {
			return nil, forgeapi.FamilyUnknown, err
		}
		refused = err
	}
	return nil, forgeapi.FamilyUnknown, undetected(refused)
}

// detection is the order families are asked in: the header precedence, GitLab's
// before GitHub's, then the Gitea family's version body.
var detection = [...]struct {
	open   func(*forgeapi.Connection, []forgeapi.Option) (forgeapi.Core, error)
	family forgeapi.Family
}{
	{open: constructor(gitlab.New), family: forgeapi.FamilyGitLab},
	{open: constructor(github.New), family: forgeapi.FamilyGitHub},
	{open: constructor(gitea.New), family: forgeapi.FamilyGitea},
}

// constructor adapts one family's exported constructor to the walk.
func constructor[C forgeapi.Core](build func(forgeapi.Connection, ...forgeapi.Option) (C, error)) func(*forgeapi.Connection, []forgeapi.Option) (forgeapi.Core, error) {
	return func(conn *forgeapi.Connection, opts []forgeapi.Option) (forgeapi.Core, error) {
		client, err := build(*conn, opts...)
		return client, err
	}
}

// undetected turns a failed detection into the one code a consumer's connect
// dialog branches on. It carries the real status, because this refusal DID make a
// request and identified no family, which is what separates it from the local
// group where nothing was sent.
func undetected(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		// The caller's context is alive here, so the last family's read outlived its own
		// operation deadline: no family answered, and no answer carried a status.
		return &forgeapi.Error{
			Op:        "ConnectionCaps",
			Code:      forgeapi.CodeFamilyUndetected,
			Family:    forgeapi.FamilyUnknown,
			Kind:      forgeapi.KindTransient,
			Retryable: transport.Retryable(forgeapi.KindTransient, 0),
			Message:   "no family established: the last family's connection read outlived its operation deadline",
		}
	}
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		return err
	}
	return &forgeapi.Error{
		Op:         fe.Op,
		Code:       forgeapi.CodeFamilyUndetected,
		Family:     forgeapi.FamilyUnknown,
		Status:     fe.Status,
		Kind:       fe.Kind,
		Retryable:  transport.Retryable(fe.Kind, fe.Status),
		Message:    fe.Message,
		RetryAfter: fe.RetryAfter,
		Pool:       fe.Pool,
		Remaining:  fe.Remaining,
		Reset:      fe.Reset,
		Secondary:  fe.Secondary,
		DiagID:     fe.DiagID,
	}
}
