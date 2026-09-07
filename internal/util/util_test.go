package util

import "testing"

func TestSplitList(t *testing.T) {
	got := SplitList("a, b\nc\r\nd,,")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestSplitListKeepsKeyCommentsIntact(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI some comment with spaces"
	if got := SplitList(key); len(got) != 1 || got[0] != key {
		t.Fatalf("public keys must not be split on spaces, got %v", got)
	}
}

func TestSplitPkgList(t *testing.T) {
	got := SplitPkgList("torch  torchvision,torchaudio\nuv")
	want := []string{"torch", "torchvision", "torchaudio", "uv"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
