package game

import (
	"bytes"
	"strings"
)

// Words is the game's string table, WORDS.TXT: string id N is line N,
// counted from 1 (VIStringTable::GetString). The disc carries one in DATA/;
// a patch server may put its own on the card, and the game reads that one
// instead — Sandstorm's adds "Register"/"Unregister" (1815, 1816) for its
// player search.
type Words []string

// ParseWords reads WORDS.TXT: UTF-8, an optional BOM, CRLF lines.
func ParseWords(data []byte) Words {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return Words(lines)
}

// Get returns string id n, or "" when the table does not have it.
func (w Words) Get(n int) string {
	if n < 1 || n > len(w) {
		return ""
	}
	return w[n-1]
}
