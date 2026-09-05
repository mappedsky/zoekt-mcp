package zoekt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// unquote mirrors what zoekt's tokenizer does to an atom value: outside a
// quoted region a backslash escapes the next character and both survive, and
// inside one a backslash is consumed and the character after it kept. Decoding
// an encoded value with this should return exactly what was encoded.
func unquote(t *testing.T, encoded string) string {
	t.Helper()
	if !strings.HasPrefix(encoded, `"`) {
		return encoded
	}
	body := encoded[1:]
	if !strings.HasSuffix(body, `"`) {
		t.Fatalf("quoted value is not terminated: %q", encoded)
	}
	body = body[:len(body)-1]

	var out strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
			out.WriteByte(body[i])
			continue
		}
		out.WriteByte(body[i])
	}
	return out.String()
}

// splitsIntoOneToken reports whether the encoded value would survive
// tokenization as a single atom. An unquoted space or quote would end the
// token and turn the rest of the value into a separate query term, which is
// how an escaping bug becomes a query that means something else entirely.
func splitsIntoOneToken(encoded string) bool {
	quoted := false
	for i := 0; i < len(encoded); i++ {
		switch encoded[i] {
		case '\\':
			i++
		case '"':
			quoted = !quoted
		case ' ', '\t', '\n', '\r':
			if !quoted {
				return false
			}
		}
	}
	return !quoted
}

func FuzzExactAtomEncodesOneToken(f *testing.F) {
	for _, seed := range []string{
		"", "simple", "github.com/org/repo", "a b.go", `a"b`, `back\slash`,
		"tab\there", "new\nline", "(paren)", "a+b", "*", "^$", `\\`, `""`, "  ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		atom := ExactAtom("repo", value)
		encoded, ok := strings.CutPrefix(atom, "repo:")
		if !ok {
			t.Fatalf("ExactAtom did not render a repo atom: %q", atom)
		}
		if !splitsIntoOneToken(encoded) {
			t.Fatalf("ExactAtom(%q) = %q, which does not tokenize as one atom", value, atom)
		}
		// The regexp zoekt should end up compiling.
		if got, want := unquote(t, encoded), "^"+quoteMetaForTest(value)+"$"; got != want {
			t.Fatalf("ExactAtom(%q) decodes to %q; want %q", value, got, want)
		}
	})
}

func FuzzPlainAtomEncodesOneToken(f *testing.F) {
	for _, seed := range []string{"", "python", "Emacs Lisp", "c++", `a"b`, `a\b`, "release/1.0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		atom := PlainAtom("lang", value)
		encoded, ok := strings.CutPrefix(atom, "lang:")
		if !ok {
			t.Fatalf("PlainAtom did not render a lang atom: %q", atom)
		}
		if !splitsIntoOneToken(encoded) {
			t.Fatalf("PlainAtom(%q) = %q, which does not tokenize as one atom", value, atom)
		}
		// A plain atom is matched literally, so it must decode to the input
		// unchanged; escaping it would turn a working filter into one that
		// silently matches nothing.
		if got := unquote(t, encoded); got != value {
			t.Fatalf("PlainAtom(%q) decodes to %q; want it unchanged", value, got)
		}
	})
}

// A wrong or wedged upstream must not be able to panic this process.
func FuzzSearchDecodesArbitraryResponses(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{}`), []byte(`{"Result":{"Files":[]}}`), []byte(`null`), []byte(`[`),
		[]byte(`{"Result":{"Files":[{"Content":"!!!notbase64"}]}}`),
		[]byte(`{"Result":{"MatchCount":9223372036854775807}}`),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		}))
		defer server.Close()

		client, err := New(server.URL, 5*time.Second)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// Either outcome is fine; crashing is not.
		if result, err := client.Search(context.Background(), "x", nil); err == nil && result == nil {
			t.Fatal("Search returned a nil result with no error")
		}
		if list, err := client.List(context.Background(), "r:.", nil); err == nil && list == nil {
			t.Fatal("List returned a nil list with no error")
		}
	})
}

func quoteMetaForTest(value string) string { return regexp.QuoteMeta(value) }
