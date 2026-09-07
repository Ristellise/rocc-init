// Package bait implements rocc's easter egg: rocc is not a compiler, and it
// will tell you so. Detection is signal-based, not name-based — a compile
// attempt is any argv that looks like a compiler invocation.
package bait

import (
	"path/filepath"
	"strings"
)

// IsCompileBait reports whether someone tried to use rocc as a C compiler.
func IsCompileBait(argv0 string, args []string) bool {
	switch filepath.Base(argv0) {
	case "gcc", "cc", "clang":
		return true
	}
	for _, a := range args {
		if compileSources[strings.ToLower(filepath.Ext(a))] || compileFlags[a] ||
			strings.HasPrefix(a, "-std=") || strings.HasPrefix(a, "-Wl,") {
			return true
		}
	}
	return false
}

var compileSources = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".cxx": true, ".c++": true,
	".i": true, ".ii": true, ".s": true, ".m": true, ".mm": true,
	".h": true, ".hpp": true,
}

var compileFlags = map[string]bool{
	"-Wall": true, "-Werror": true, "-Wextra": true, "-pedantic": true, "-pipe": true,
}
