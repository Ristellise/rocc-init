package main

import "testing"

func TestParseInstallArgs(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		items   []string
		index   string
		pkgs    string
		wantErr bool
	}{
		{name: "single", in: []string{"uv"}, items: []string{"uv"}},
		{name: "multiple", in: []string{"uv", "pytorch"}, items: []string{"uv", "pytorch"}},
		{name: "index value", in: []string{"pytorch", "--index", "https://x/y"}, items: []string{"pytorch"}, index: "https://x/y"},
		{name: "index inline", in: []string{"pytorch", "--index=https://x/y"}, items: []string{"pytorch"}, index: "https://x/y"},
		{name: "pkgs value", in: []string{"pytorch", "--pkgs", "torch,torchvision"}, items: []string{"pytorch"}, pkgs: "torch,torchvision"},
		{name: "pkgs inline", in: []string{"pytorch", "--pkgs=torch"}, items: []string{"pytorch"}, pkgs: "torch"},
		{name: "both flags", in: []string{"--index=https://x/y", "pytorch", "--pkgs", "torch"}, items: []string{"pytorch"}, index: "https://x/y", pkgs: "torch"},
		{name: "missing value", in: []string{"pytorch", "--index"}, wantErr: true},
		{name: "nothing to install", in: nil, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseInstallArgs(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.index != c.index || got.pkgs != c.pkgs {
				t.Fatalf("flags: got index=%q pkgs=%q, want index=%q pkgs=%q", got.index, got.pkgs, c.index, c.pkgs)
			}
			if len(got.items) != len(c.items) {
				t.Fatalf("items: got %v want %v", got.items, c.items)
			}
			for i := range c.items {
				if got.items[i] != c.items[i] {
					t.Fatalf("items: got %v want %v", got.items, c.items)
				}
			}
		})
	}
}
