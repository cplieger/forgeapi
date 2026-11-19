package transport

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cplieger/go/masked/maskedtest"
)

// TestWhatAConnectionRedactsWithIsHeldMasked holds the secrets a connection keeps
// to redact its exchanges' text with, and the tokens its requests carried lately,
// masked, so a dump of a client by reflection prints none of them.
func TestWhatAConnectionRedactsWithIsHeldMasked(t *testing.T) {
	found := maskedtest.Unmasked(maskedtest.Allow{}, reflect.TypeFor[quoter](), reflect.TypeFor[recentTokens]())
	if len(found) != 0 {
		t.Errorf("text held in clear:\n%s", strings.Join(found, "\n"))
	}
}
