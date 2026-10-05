package families

import (
	"context"
	"errors"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/gitea"
	"github.com/cplieger/forgeapi/github"
	"github.com/cplieger/forgeapi/gitlab"
)

// Open builds a client for conn by asking GitLab's connection read, then GitHub's, then the
// Gitea family's, and answers the first family whose read establishes it beside that family's
// client, its connection capabilities held; an optional role it lacks fails a type assertion.
// It costs that read plus one request per family asked first, at most 2 on GitLab, 3 on GitHub
// and 5 on the Gitea family, the reads ahead counted by no budget the caller holds; a caller that
// knows its family calls that family's constructor. No family established is a [*forgeapi.Error]
// with [forgeapi.CodeFamilyUndetected], [forgeapi.FamilyUnknown] and the last read's status; the
// caller's context ending answers its sentinel, and a constructor's refusal returns unchanged.
// Every client Open built and does not answer is closed before it returns, since the caller
// never holds one; the client it answers is the caller's to close.
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
			Retryable: true,
			Message:   "no family established: the last family's connection read outlived its operation deadline",
		}
	}
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		return err
	}
	return &forgeapi.Error{
		Op:      fe.Op,
		Code:    forgeapi.CodeFamilyUndetected,
		Family:  forgeapi.FamilyUnknown,
		Status:  fe.Status,
		Kind:    fe.Kind,
		Message: fe.Message,
		DiagID:  fe.DiagID,
	}
}
