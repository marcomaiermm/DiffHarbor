// Package text shortens strings for prompts, comments and logs.
package text

import (
	"strconv"
	"strings"
)

// Truncate cuts s to at most n runes.
func Truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// OneLine collapses whitespace and cuts s to n runes.
func OneLine(s string, n int) string { return Truncate(strings.Join(strings.Fields(s), " "), n) }

// Tail is the last 300 bytes of s.
func Tail(s string) string { return s[max(0, len(s)-300):] }

// Count is n with the noun, plural unless n is 1: "1 file", "3 files".
func Count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
