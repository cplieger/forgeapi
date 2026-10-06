package forgeapi_test

import (
	"errors"
	"fmt"

	"github.com/cplieger/forgeapi"
)

// localRefusal describes how err differs from the refusal a validator answers
// before any request: a *forgeapi.Error carrying code and no operation, family or
// status. It returns nil when err is that refusal.
func localRefusal(err error, code string) error {
	var fe *forgeapi.Error
	if !errors.As(err, &fe) {
		return fmt.Errorf("got %v, want a *forgeapi.Error with code %q", err, code)
	}
	if fe.Code != code {
		return fmt.Errorf("got code %q, want %q", fe.Code, code)
	}
	if fe.Op != "" || fe.Family != forgeapi.FamilyUnknown || fe.Status != 0 {
		return fmt.Errorf("got Op %q family %v status %d, want the local refusal's empty three", fe.Op, fe.Family, fe.Status)
	}
	return nil
}
