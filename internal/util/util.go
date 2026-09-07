// Package util holds rocc-init's small shared helpers: logging, env-var
// parsing and shell quoting.
package util

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Logf logs to stderr, which is where docker collects container output.
func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "rocc: %s %s\n", time.Now().UTC().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// Fatalf logs and exits non-zero. Reserve it for boot-time dead ends.
func Fatalf(format string, args ...any) {
	Logf(format, args...)
	os.Exit(1)
}

// EnvOr returns the value of key, or def when unset or empty.
func EnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// SplitList splits an env value on commas and newlines. Use for values whose
// fields may contain spaces (e.g. public keys with comments).
func SplitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// SplitPkgList splits an env value on commas, newlines and spaces. Use for
// package lists only; a public key comment must never be split.
func SplitPkgList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	}) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ShQuote quotes s for safe interpolation into a POSIX shell command.
func ShQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
