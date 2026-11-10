package creds_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cplieger/forgeapi/v2/creds"
)

// mintedToken is one InstallationToken a source minted, holding ghs_token1.
func mintedToken(t *testing.T) creds.InstallationToken {
	t.Helper()
	var tok creds.InstallationToken
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		var err error
		if tok, err = appSource(t, g, creds.InstallationByID(42)).Mint(t.Context()); err != nil {
			t.Fatalf("Setup: Mint() = %v", err)
		}
	})
	return tok
}

func TestSecret_prints_no_part_of_the_token(t *testing.T) {
	tok := mintedToken(t)
	const secret = "ghs_token1"
	if got := tok.Token.Reveal(); got != secret {
		t.Fatalf("Reveal() = %q, want %q", got, secret)
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("minted", "token", tok, "secret", tok.Token)
	marshalled, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings := map[string]string{
		"slog":         logged.String(),
		"json.Marshal": string(marshalled),
		"String":       tok.Token.String(),
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		renderings[verb+" of the token"] = fmt.Sprintf(verb, tok)
		renderings[verb+" of the secret"] = fmt.Sprintf(verb, tok.Token)
	}
	for name, out := range renderings {
		if strings.Contains(out, "token1") || strings.Contains(out, fmt.Sprintf("%x", secret)) {
			t.Errorf("%s = %q, which carries the token", name, out)
		}
	}
	if got := fmt.Sprint(tok.Token); got != "[redacted]" {
		t.Errorf("fmt.Sprint(Token) = %q, want [redacted]", got)
	}
}

// A source holds its cached token as a Secret, so printing or logging the
// source carries none of it.
func TestAppSource_prints_no_part_of_its_cached_token(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newFakeGitHub(t, mintsAt(t, 42, time.Hour))
		s := appSource(t, g, creds.InstallationByID(42))
		const secret = "ghs_token1"
		if got, err := s.Token(t.Context()); err != nil || got != secret {
			t.Fatalf("Setup: Token() = %q, %v, want %s", got, err, secret)
		}
		var text, structured bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Info("source", "source", s)
		slog.New(slog.NewJSONHandler(&structured, nil)).Info("source", "source", s)
		renderings := map[string]string{"slog text": text.String(), "slog JSON": structured.String()}
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			renderings[verb] = fmt.Sprintf(verb, s)
		}
		for name, out := range renderings {
			if strings.Contains(out, "token1") || strings.Contains(out, fmt.Sprintf("%x", secret)) {
				t.Errorf("%s of the source = %q, which carries the token", name, out)
			}
		}
	})
}

// A printer walking fields by reflection reaches only masked bytes.
func TestSecret_holds_the_token_masked(t *testing.T) {
	tok := mintedToken(t)
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path+".*")
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				b := make([]byte, v.Len())
				for i := range b {
					b[i] = byte(v.Index(i).Uint())
				}
				if bytes.Contains(b, []byte("token1")) {
					t.Errorf("%s holds the token in clear", path)
				}
			}
		case reflect.String:
			if strings.Contains(v.String(), "token1") {
				t.Errorf("%s holds the token in clear", path)
			}
		}
	}
	walk(reflect.ValueOf(tok), "InstallationToken")
}

func TestSecret_zero_value(t *testing.T) {
	var s creds.Secret
	if !s.IsZero() || s.Reveal() != "" || fmt.Sprint(s) != "" {
		t.Errorf("the zero Secret: IsZero %v, Reveal %q, prints %q; want true, empty, empty", s.IsZero(), s.Reveal(), fmt.Sprint(s))
	}
	if tok := mintedToken(t); tok.Token.IsZero() {
		t.Error("a minted token's Secret reads as zero")
	}
}
