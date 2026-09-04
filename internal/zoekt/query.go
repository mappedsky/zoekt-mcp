package zoekt

import (
	"regexp"
	"strings"
)

// Zoekt parses every bare atom value as a regular expression, so any value
// taken from a tool argument has to be escaped before it is interpolated.
// Two layers are involved and they undo each other if applied in the wrong
// order:
//
//   - Outside quotes the tokenizer copies a backslash and the character after
//     it verbatim, so an escaped regexp survives as written.
//   - Inside a quoted literal the tokenizer strips one backslash level, so the
//     same escaped regexp has to have its backslashes doubled to survive.
//
// Quoting is only needed for values carrying whitespace or a double quote,
// which the tokenizer would otherwise treat as a token break or a string
// literal start.

// needsQuoting reports whether raw would be mis-tokenized unquoted.
func needsQuoting(raw string) bool {
	return strings.ContainsAny(raw, " \t\n\r\"")
}

// Atom renders prefix:value for a value that is already a regular expression,
// encoding it so zoekt's tokenizer hands the regexp parser exactly rawRegexp.
func Atom(prefix, rawRegexp string) string {
	return prefix + ":" + encodeValue(rawRegexp)
}

// ExactAtom renders prefix:value matching value literally and in full. Use it
// only for prefixes zoekt compiles as a regular expression: repo and file.
func ExactAtom(prefix, value string) string {
	return Atom(prefix, "^"+regexp.QuoteMeta(value)+"$")
}

// PlainAtom renders prefix:value for the prefixes zoekt does not treat as a
// regular expression, where anchoring or escaping the value silently breaks
// the query rather than failing it:
//
//   - lang resolves its value through a language name/alias table and compiles
//     to a constant false when the lookup misses, so "lang:^python$" matches
//     nothing at all.
//   - branch is a substring test against the branch name, so "branch:^main$"
//     looks for that literal text inside the name and never matches.
func PlainAtom(prefix, value string) string {
	return prefix + ":" + encodeValue(value)
}

// WordRegexp is a whole-word match for an identifier. This is the same
// heuristic other search-backed code navigation uses: it finds every mention
// of the name, definitions and uses alike, not a resolved reference set.
func WordRegexp(name string) string {
	return `\b` + regexp.QuoteMeta(name) + `\b`
}

// MatchAllRepos matches every indexed repository. An empty query is rejected
// by zoekt, so listing everything needs an atom that always matches.
const MatchAllRepos = "r:."

func encodeValue(rawRegexp string) string {
	if !needsQuoting(rawRegexp) {
		return rawRegexp
	}
	quoted := strings.ReplaceAll(rawRegexp, `\`, `\\`)
	quoted = strings.ReplaceAll(quoted, `"`, `\"`)
	return `"` + quoted + `"`
}

// Join assembles non-empty atoms into a single conjunctive query.
func Join(atoms ...string) string {
	kept := make([]string, 0, len(atoms))
	for _, atom := range atoms {
		if trimmed := strings.TrimSpace(atom); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " ")
}
