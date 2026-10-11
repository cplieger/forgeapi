package github

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cplieger/forgeapi"
	"github.com/cplieger/forgeapi/internal/transport"
)

// runWindow is the result window GitHub documents for one created filter, which it
// serves through the first whole page reaching it; internal/spec owns the measured edge:
// https://docs.github.com/en/rest/actions/workflow-runs#list-workflow-runs-for-a-repository
const runWindow = 1000

func windowPages(perPage int) int {
	return (runWindow + perPage - 1) / perPage
}

const runWalkPrefix = "w"

const runWalkFixed = 8

// The created filters a run walk pages through. Each value is written into a
// continuation, so the order is part of its encoding.
const (
	openRange   int64 = iota // the bound onwards
	boundRange               // the bound to upper
	secondRange              // seen alone
)

// runWalk is where a run walk under a since bound stands: page counts the walk,
// window the pages of its current range. Under a bound range or the open one, ids are
// the runs of seen, the oldest second its last page served, that the range answered;
// under a second's own range, the ones the walk answered before it that the range has
// not served yet. omitted counts the runs of seconds behind the walk that neither it
// nor their own range answered.
type runWalk struct {
	call    transport.PageCall
	ids     []int64
	kind    int64
	upper   int64
	seen    int64
	since   int64
	page    int
	window  int
	fetched int
	omitted int
}

// runWalkAt reads where a run walk stands from its continuation, the empty one being
// the first page of the open range. It refuses, before any request, a continuation of
// another encoding or call and one whose numbers break a relation every continuation
// the walk mints keeps between them.
func runWalkAt(call transport.PageCall, set *forgeapi.ListSettings) (runWalk, error) {
	since := set.Since.Unix()
	if set.After == "" {
		return runWalk{call: call, since: since, page: 1, window: 1}, nil
	}
	count := strings.Count(string(set.After), ".") - 1
	if count < runWalkFixed {
		return runWalk{}, transport.Local(forgeapi.CodeCursorInvalid, "continuation is not one of this library's own")
	}
	p, err := call.ResumeWide(set.After, runWalkPrefix, count)
	if err != nil {
		return runWalk{}, err
	}
	w, ok := walkFrom(call, p, since)
	perPage := min(set.PageBound, restPerPageMax)
	if !ok || !w.ranged() || w.window > windowPages(perPage) || !w.counted(perPage) || !w.roomy(perPage) {
		return runWalk{}, transport.Local(forgeapi.CodeCursorInvalid, "continuation names no position a run walk under this bound reaches")
	}
	return w, nil
}

func walkFrom(call transport.PageCall, p []int64, since int64) (runWalk, bool) {
	page, window, fetched, omitted, n := p[0], p[1], p[4], p[6], p[7]
	ids, ok := idsFrom(p[runWalkFixed:])
	if !ok || n != int64(len(ids)) || min(page, window, fetched, omitted) < 0 ||
		page > math.MaxInt || window > math.MaxInt || fetched > math.MaxInt || omitted > math.MaxInt {
		return runWalk{}, false
	}
	return runWalk{
		call: call, ids: ids, kind: p[2], upper: p[3], seen: p[5], since: since,
		page: int(page), window: int(window), fetched: int(fetched), omitted: int(omitted),
	}, true
}

// ranged reports whether w's range stands where mint puts one: the open range on every
// page before the walk's first narrower range, with no second behind it, and a range's
// ids naming a second in it that one of its earlier pages served.
func (w *runWalk) ranged() bool {
	if w.page < 1 || w.window < 1 || w.omitted < 0 || w.omitted > 0 && w.kind == openRange {
		return false
	}
	switch w.kind {
	case openRange:
		return w.window == w.page && w.upper == 0 && w.servedSeen(math.MaxInt64)
	case boundRange:
		return w.window < w.page && w.upper >= w.since && w.servedSeen(w.upper)
	case secondRange:
		return w.window < w.page && w.upper == 0 && w.seen >= w.since
	}
	return false
}

// servedSeen reports whether seen and the ids beside it are none, or name a second of
// the range from the bound to upper that a page of the range before w's served.
func (w *runWalk) servedSeen(upper int64) bool {
	if len(w.ids) == 0 || w.window == 1 {
		return w.seen == 0 && len(w.ids) == 0
	}
	return w.seen >= w.since && w.seen <= upper
}

// counted reports whether the runs w's walk answered fit the pages before w's, each
// answering at most perPage, and include its ids.
func (w *runWalk) counted(perPage int) bool {
	before := w.page - 1
	return w.fetched >= len(w.ids) && (w.fetched == 0 || (w.fetched-1)/perPage < before)
}

// roomy reports whether w's page and answer counts leave room for the most the longest
// range fits reserves after the page following w's adds to them.
func (w *runWalk) roomy(perPage int) bool {
	return w.page < math.MaxInt-runWindow-1 && w.fetched <= math.MaxInt-runWindow-3*perPage
}

// idsFrom is the ascending ids deltas write, each a distance from the one before:
// false where a distance or a sum is not positive.
func idsFrom(deltas []int64) ([]int64, bool) {
	ids := make([]int64, 0, len(deltas))
	var id int64
	for _, delta := range deltas {
		id += delta
		if delta <= 0 || id <= 0 {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// mint is the continuation resuming the walk where w stands, the ids written as each
// one's distance from the one before, which keeps a busy second's runs short.
func (w *runWalk) mint() forgeapi.Cursor {
	position := make([]int64, 0, runWalkFixed+len(w.ids))
	position = append(position, int64(w.page), int64(w.window), w.kind, w.upper, int64(w.fetched), w.seen, int64(w.omitted), int64(len(w.ids)))
	for i, id := range w.ids {
		if i > 0 {
			id -= w.ids[i-1]
		}
		position = append(position, id)
	}
	return w.call.MintWide(runWalkPrefix, position...)
}

// fits reports whether w's continuation stays inside the cursor's byte cap, and so
// does the longest one a range of seen alone, carrying w's ids, mints: that range
// serves at most one window and starts on the page after w's at the latest.
func (w *runWalk) fits(perPage int) bool {
	longest := runWalk{
		call: w.call, ids: w.ids, kind: secondRange, seen: w.seen,
		page: w.page + runWindow + 1, window: runWindow, fetched: w.fetched + runWindow + 2*perPage, omitted: w.omitted,
	}
	return forgeapi.ValidateCursor(w.mint()) == nil && forgeapi.ValidateCursor(longest.mint()) == nil
}

// query is the request for the page w stands at. created takes the search syntax
// at whole seconds, ">=" for the open range and an inclusive range after it:
// https://docs.github.com/en/search-github/getting-started-with-searching-on-github/understanding-the-search-syntax#query-for-dates
func (w *runWalk) query(set *forgeapi.ListSettings) url.Values {
	created := ">=" + set.Since.Format("2006-01-02T15:04:05") + "+00:00"
	switch w.kind {
	case boundRange:
		created = stamp(w.since) + ".." + stamp(w.upper)
	case secondRange:
		created = stamp(w.seen) + ".." + stamp(w.seen)
	}
	return url.Values{
		keyPage:    {strconv.Itoa(w.window)},
		keyPerPage: {strconv.Itoa(set.PageBound)},
		"created":  {created},
	}
}

func stamp(second int64) string {
	return time.Unix(second, 0).UTC().Format(time.RFC3339)
}

// unseen answers rows but the runs w carries, each dropped from w's ids once its
// second's own range serves it.
func (w *runWalk) unseen(rows []restWorkflowRun) []restWorkflowRun {
	if w.kind != secondRange || len(w.ids) == 0 {
		return rows
	}
	ids := slices.Clone(w.ids)
	fresh := slices.DeleteFunc(slices.Clone(rows), func(r restWorkflowRun) bool {
		i, found := slices.BinarySearch(ids, r.ID)
		if found && r.CreatedAt.Unix() == w.seen {
			ids = slices.Delete(ids, i, i+1)
			return true
		}
		return false
	})
	w.ids = ids
	return fresh
}

// step is what the page w stands at does with its rows, given how many rows its range
// has served through it, the total the range states and whether it relates a next
// page: the rows it answers, the walk after it, nil where the walk ends, and
// whether the range ends short of a total no narrower range reaches. Only the window's
// pages are followed: GitHub serves none past them, and a continuation naming one is
// refused.
func (w *runWalk) step(rows []restWorkflowRun, served, total int, more bool, perPage int) ([]restWorkflowRun, *runWalk, bool) {
	fresh := w.unseen(rows)
	switch {
	case more && w.window < windowPages(perPage):
		return w.advance(fresh, perPage)
	case w.kind == secondRange:
		return w.below(fresh, served, total)
	case served < total:
		return w.narrow(fresh)
	}
	return fresh, nil, false
}

// below ends the second's own range w stands in and goes on below that second. The
// runs of it neither the range nor the walk before it answered count as omitted where
// the range served the whole window, and end the walk short of its total otherwise.
func (w *runWalk) below(rows []restWorkflowRun, served, total int) ([]restWorkflowRun, *runWalk, bool) {
	if left := total - served - len(w.ids); left > 0 {
		if served < runWindow {
			return nil, nil, true
		}
		w.omitted += min(left, math.MaxInt-w.omitted)
	}
	if w.seen == w.since {
		return rows, nil, false
	}
	return rows, w.boundTo(w.seen-1, len(rows)), false
}

// advance answers rows and goes on to the next page of w's range, unless the ids of
// their oldest second would not fit a continuation: that second is then held back for
// a range of its own.
func (w *runWalk) advance(rows []restWorkflowRun, perPage int) ([]restWorkflowRun, *runWalk, bool) {
	after := *w
	after.page++
	after.window++
	after.fetched += len(rows)
	if w.kind == secondRange || len(rows) == 0 {
		return rows, &after, false
	}
	oldest := oldestSecond(rows)
	after.seen, after.ids = oldest, idsAt(rows, oldest)
	if oldest == w.seen {
		after.ids = slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(w.ids), after.ids...))))
	}
	if after.fits(perPage) {
		return rows, &after, false
	}
	return w.alone(rows, oldest)
}

// narrow is the walk after a page that ends w's range short of its total. The page
// holds back its oldest second, for the range from the bound through it where no
// earlier page of the range served that second and the range does not already end at
// it, and for that second's own range otherwise. A page with no row holds back the
// previous page's oldest second; a range that served no row is short of a total
// nothing explains.
func (w *runWalk) narrow(rows []restWorkflowRun) ([]restWorkflowRun, *runWalk, bool) {
	if len(rows) == 0 {
		if w.seen == 0 {
			return nil, nil, true
		}
		return w.alone(nil, w.seen)
	}
	oldest := oldestSecond(rows)
	if oldest == w.seen || w.kind == boundRange && oldest == w.upper {
		return w.alone(rows, oldest)
	}
	answer := slices.DeleteFunc(slices.Clone(rows), func(r restWorkflowRun) bool { return r.CreatedAt.Unix() == oldest })
	return answer, w.boundTo(oldest, len(answer)), false
}

func (w *runWalk) boundTo(upper int64, answered int) *runWalk {
	return &runWalk{
		call: w.call, kind: boundRange, upper: upper, since: w.since,
		page: w.page + 1, window: 1, fetched: w.fetched + answered, omitted: w.omitted,
	}
}

// alone answers rows but those of second, which the walk goes on to under that
// second's own range, carrying the ids of it w's range answered.
func (w *runWalk) alone(rows []restWorkflowRun, second int64) ([]restWorkflowRun, *runWalk, bool) {
	answer := slices.DeleteFunc(slices.Clone(rows), func(r restWorkflowRun) bool { return r.CreatedAt.Unix() == second })
	next := &runWalk{
		call: w.call, kind: secondRange, seen: second, since: w.since,
		page: w.page + 1, window: 1, fetched: w.fetched + len(answer), omitted: w.omitted,
	}
	if second == w.seen {
		next.ids = w.ids
	}
	return answer, next, false
}

func oldestSecond(rows []restWorkflowRun) int64 {
	oldest := rows[0].CreatedAt.Unix()
	for i := range rows {
		oldest = min(oldest, rows[i].CreatedAt.Unix())
	}
	return oldest
}

func idsAt(rows []restWorkflowRun, second int64) []int64 {
	var ids []int64
	for i := range rows {
		if rows[i].CreatedAt.Unix() == second {
			ids = append(ids, rows[i].ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// listRunsSince is [Client.ListRuns] under a since bound; [runWalk.step] owns where
// each page takes the walk.
func (c *Client) listRunsSince(ctx context.Context, op string, repo forgeapi.RepoRef, set *forgeapi.ListSettings) (forgeapi.Page[forgeapi.Run], error) {
	walk, err := runWalkAt(c.core.PageCall(op, repo, set), set)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	if bounded, ok := c.unpagedList[forgeapi.Run](walk.mint()); ok {
		return bounded, nil
	}
	var moved rename
	runs, resp, err := c.readRuns(ctx, op, repo, walk.query(set), &moved)
	if err != nil {
		if deferred, ok := c.deferredPage[forgeapi.Run](err); ok {
			return deferred, nil
		}
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	kept, err := c.keptRuns(ctx, op, runs.Runs, set, resp.Status)
	if err != nil {
		return forgeapi.Page[forgeapi.Run]{}, err
	}
	n := min(set.PageBound, restPerPageMax)
	served := (walk.window-1)*n + len(runs.Runs)
	answer, next, short := walk.step(kept, served, runs.TotalCount, hasNextLink(resp.Header), n)
	if short {
		return forgeapi.Page[forgeapi.Run]{}, c.refuseRun(ctx, op, resp.Status, "the run listing stated more runs than it served where no second holds more than its result window, so the rest cannot be reached")
	}
	page := forgeapi.Page[forgeapi.Run]{Items: c.normalizeRuns(answer, repo), Successor: moved.to}
	if next == nil && walk.omitted > 0 {
		page.Partial = c.partial(forgeapi.PartialResultWindow, walk.fetched+len(answer))
		page.Partial.OmittedAtLeast = walk.omitted
		return page, nil
	}
	if next != nil {
		page.Next = next.mint()
	}
	page.Partial = c.pagePartial(len(runs.Runs), page.Next, walk.page)
	return page, nil
}
