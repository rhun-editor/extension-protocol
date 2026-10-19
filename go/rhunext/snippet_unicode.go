package rhunext

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type snippetRuneRange struct{ lo, hi rune }
type snippetFoldEntry struct{ from, to rune }
type snippetCaseEntry struct {
	from rune
	text string
}

func snippetUnicodeScalar(cp rune) bool {
	return cp >= 0 && cp <= utf8.MaxRune && (cp < 0xd800 || cp > 0xdfff)
}

// Shared immutable ranges. Callers must copy before normalizing or combining sets.
func snippetUnicodeProperty(expression string) ([]snippetRuneRange, bool) {
	key := ""
	if property, value, hasValue := strings.Cut(expression, "="); hasValue {
		switch property {
		case "gc", "General_Category":
			key = "gc:"
		case "sc", "Script":
			key = "sc:"
		case "scx", "Script_Extensions":
			key = "scx:"
		default:
			return nil, false
		}
		key += value
	} else {
		key = "gc:" + expression
		if _, ok := snippetUnicodeAliases[key]; !ok {
			key = "bin:" + expression
		}
	}
	canonical, ok := snippetUnicodeAliases[key]
	if !ok {
		return nil, false
	}
	ranges, ok := snippetUnicodeProperties[canonical]
	return ranges, ok
}
func snippetUnicodeContains(ranges []snippetRuneRange, cp rune) bool {
	if !snippetUnicodeScalar(cp) {
		return false
	}
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi >= cp })
	return i < len(ranges) && ranges[i].lo <= cp
}
func snippetUnicodeFold(cp rune) rune {
	i := sort.Search(len(snippetUnicodeFolds), func(i int) bool { return snippetUnicodeFolds[i].from >= cp })
	if i < len(snippetUnicodeFolds) && snippetUnicodeFolds[i].from == cp {
		return snippetUnicodeFolds[i].to
	}
	return cp
}

// Empty means identity. Mappings implement unconditional default Unicode case;
// context rules such as final sigma belong to the string format executor.
func snippetUnicodeCase(cp rune, upper bool) string {
	table := snippetUnicodeLower[:]
	if upper {
		table = snippetUnicodeUpper[:]
	}
	i := sort.Search(len(table), func(i int) bool { return table[i].from >= cp })
	if i < len(table) && table[i].from == cp {
		return table[i].text
	}
	return ""
}
