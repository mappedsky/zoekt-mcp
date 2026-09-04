package zoekt

import "testing"

func TestExactAtomEscapesRegexpMetacharacters(t *testing.T) {
	got := ExactAtom("repo", "github.com/example/repo")
	want := `repo:^github\.com/example/repo$`
	if got != want {
		t.Fatalf("ExactAtom = %q; want %q", got, want)
	}
}

func TestExactAtomQuotesWhitespaceAndDoublesBackslashes(t *testing.T) {
	// Inside a quoted literal zoekt's tokenizer strips one backslash level, so
	// the encoded form has to carry two for the regexp parser to see one.
	got := ExactAtom("file", "docs/release notes.md")
	want := `file:"^docs/release notes\\.md$"`
	if got != want {
		t.Fatalf("ExactAtom = %q; want %q", got, want)
	}
}

func TestExactAtomQuotesEmbeddedDoubleQuote(t *testing.T) {
	got := ExactAtom("file", `a"b.go`)
	want := `file:"^a\"b\\.go$"`
	if got != want {
		t.Fatalf("ExactAtom = %q; want %q", got, want)
	}
}

func TestWordRegexpEscapesTheIdentifier(t *testing.T) {
	got := WordRegexp("Parse.Config")
	want := `\bParse\.Config\b`
	if got != want {
		t.Fatalf("WordRegexp = %q; want %q", got, want)
	}
}

func TestAtomLeavesARawRegexpAlone(t *testing.T) {
	got := Atom("sym", `\bhandle\b`)
	want := `sym:\bhandle\b`
	if got != want {
		t.Fatalf("Atom = %q; want %q", got, want)
	}
}

func TestJoinDropsEmptyAtoms(t *testing.T) {
	got := Join("lang:go", "", "  ", "case:yes")
	want := "lang:go case:yes"
	if got != want {
		t.Fatalf("Join = %q; want %q", got, want)
	}
}

func TestPlainAtomDoesNotEscapeOrAnchor(t *testing.T) {
	if got := PlainAtom("lang", "c++"); got != "lang:c++" {
		t.Fatalf("PlainAtom = %q; want lang:c++", got)
	}
	if got := PlainAtom("branch", "release/1.0"); got != "branch:release/1.0" {
		t.Fatalf("PlainAtom = %q; want branch:release/1.0", got)
	}
}

func TestPlainAtomStillQuotesWhitespace(t *testing.T) {
	if got := PlainAtom("lang", "Emacs Lisp"); got != `lang:"Emacs Lisp"` {
		t.Fatalf("PlainAtom = %q; want the value quoted", got)
	}
}
