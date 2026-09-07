package bait

import "testing"

func TestIsCompileBait(t *testing.T) {
	cases := []struct {
		argv0 string
		args  []string
		want  bool
	}{
		// compile-looking args, detected by signal
		{"/usr/local/bin/rocc", []string{"main.c"}, true},
		{"/usr/local/bin/rocc", []string{"-Wall", "main.c", "-o", "main"}, true},
		{"/usr/local/bin/rocc", []string{"src/app.cpp", "-lpthread"}, true},
		{"/usr/local/bin/rocc", []string{"-std=c99", "x.cc"}, true},
		{"/usr/local/bin/rocc", []string{"-Wl,-rpath,/x", "y.c"}, true},
		// evil symlinks
		{"/usr/bin/gcc", nil, true},
		{"/usr/bin/cc", []string{"-O2", "x.c"}, true},
		{"/usr/bin/clang", nil, true},
		// legit usage stays legit — including rocc as an entrypoint
		{"/usr/local/bin/rocc", []string{"gpu"}, false},
		{"/usr/local/bin/rocc", nil, false},
		{"/usr/local/bin/rocc", []string{"install", "uv"}, false},
		{"/usr/local/bin/rocc", []string{"sleep", "infinity"}, false},
		{"/usr/local/bin/rocc", []string{"sh", "-c", "echo hi"}, false},
		{"/usr/local/bin/rocc", []string{"python3", "script.py"}, false},
	}
	for _, c := range cases {
		if got := IsCompileBait(c.argv0, c.args); got != c.want {
			t.Errorf("IsCompileBait(%q, %v) = %v, want %v", c.argv0, c.args, got, c.want)
		}
	}
}
