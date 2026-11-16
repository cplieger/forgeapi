package transport

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cplieger/forgeapi/v3"
)

// MaxRunPages is the most pages a re-run's resolving read walks for a run that
// failed. The GitHub, Gitea and Forgejo prices for
// [forgeapi.PullRequests.RerunFailedChecks] in internal/spec count this many reads.
const MaxRunPages = 3

// PickRun walks a re-run's resolving read from page 1, each page read by read at
// size rows, and answers the first row on which pick reports both that it carries
// the head and that it failed or was cancelled. A page shorter than size ends the
// walk, and so does page [MaxRunPages]. With no such row it refuses before any
// write: [forgeapi.CodeRunPageTruncated] after a full last page, else
// [forgeapi.CodeStaleHead] for a pin no row carried, else
// [forgeapi.CodeRunNotFailed], each naming the row by noun.
func PickRun[T any](
	ctx context.Context, c *Conn, op, noun string, size int, pinned bool,
	read func(page int) ([]T, error), pick func(*T) (onHead, rerunnable bool),
) (T, error) {
	var zero T
	carried := false
	for page := 1; page <= MaxRunPages; page++ {
		rows, err := read(page)
		if err != nil {
			return zero, err
		}
		for i := range rows {
			onHead, rerunnable := pick(&rows[i])
			if onHead && rerunnable {
				return rows[i], nil
			}
			carried = carried || onHead
		}
		if len(rows) < size {
			return zero, c.runRefusal(ctx, op, noun, pinned, carried)
		}
	}
	return zero, c.Fail(ctx, op, REST(http.MethodGet), forgeapi.CodeRunPageTruncated, forgeapi.KindUpstream, http.StatusOK,
		"no "+noun+" on the first "+strconv.Itoa(MaxRunPages)+" pages of this head's, read at "+strconv.Itoa(size)+
			" rows each, failed or was cancelled, and the last came back full, so the list continues past it")
}

// runRefusal is [PickRun]'s answer for a walk that reached a short page with no run
// to re-run, carried reporting whether any row read was on the head.
func (c *Conn) runRefusal(ctx context.Context, op, noun string, pinned, carried bool) error {
	code, message := forgeapi.CodeRunNotFailed, "no "+noun+" on this head failed or was cancelled"
	switch {
	case carried:
	case pinned:
		code, message = forgeapi.CodeStaleHead, "no "+noun+" carries the head this caller pinned"
	default:
		message = "no " + noun + " carries the pull request's current head"
	}
	return c.Fail(ctx, op, REST(http.MethodGet), code, forgeapi.KindConflict, http.StatusOK, message)
}
