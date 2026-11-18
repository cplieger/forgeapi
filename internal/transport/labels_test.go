package transport

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/forgeapi/v3"
)

func TestDistinctNamesKeepsTheFirstOfEachNamedLabelInOrder(t *testing.T) {
	got := DistinctNames([]string{"b", "a", "b", "", "a", ""})
	if want := []string{"b", "a"}; !slices.Equal(got, want) {
		t.Errorf("DistinctNames = %q, want %q", got, want)
	}
	if got := DistinctNames(nil); len(got) != 0 {
		t.Errorf("DistinctNames(nil) = %q, want none", got)
	}
}

func TestAfterRemovalsNamesWhatARefusalLeftRemoved(t *testing.T) {
	t.Run("with_a_message", func(t *testing.T) {
		fe := &forgeapi.Error{Kind: forgeapi.KindForbidden, Message: "Forbidden"}
		err := AfterRemovals(fe, []string{"a", "b"})
		if err != error(fe) || fe.Message != "Forbidden; removed before this refusal: a, b" {
			t.Errorf("AfterRemovals = %v with message %q, want the same error naming a and b after its own words", err, fe.Message)
		}
	})
	t.Run("with_no_message", func(t *testing.T) {
		fe := &forgeapi.Error{Kind: forgeapi.KindForbidden}
		if err := AfterRemovals(fe, []string{"a"}); err != error(fe) || fe.Message != "removed before this refusal: a" {
			t.Errorf("AfterRemovals = %v with message %q, want it naming a alone", err, fe.Message)
		}
	})
	t.Run("nothing_removed", func(t *testing.T) {
		fe := &forgeapi.Error{Kind: forgeapi.KindForbidden, Message: "Forbidden"}
		if err := AfterRemovals(fe, nil); err != error(fe) || fe.Message != "Forbidden" {
			t.Errorf("AfterRemovals with nothing removed = %v with message %q, want it unchanged", err, fe.Message)
		}
	})
	t.Run("a_name_holding_a_line_break", func(t *testing.T) {
		fe := &forgeapi.Error{Kind: forgeapi.KindForbidden, Message: "Forbidden"}
		if err := AfterRemovals(fe, []string{"bug\nfix"}); err != error(fe) || strings.ContainsAny(fe.Message, "\r\n") ||
			!strings.Contains(fe.Message, "bug") || !strings.Contains(fe.Message, "fix") {
			t.Errorf("AfterRemovals = %v with message %q, want one line still naming the label", err, fe.Message)
		}
	})
	t.Run("a_context_sentinel", func(t *testing.T) {
		if err := AfterRemovals(context.Canceled, []string{"a"}); !errors.Is(err, context.Canceled) || err.Error() != context.Canceled.Error() {
			t.Errorf("AfterRemovals(context.Canceled) = %v, want the sentinel as it is", err)
		}
	})
}
