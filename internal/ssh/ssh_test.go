package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteMotd(t *testing.T) {
	dir := t.TempDir()
	orig := motdPath
	defer func() { motdPath = orig }()

	// fresh file: banner only
	motdPath = filepath.Join(dir, "motd")
	if err := writeMotd(); err != nil {
		t.Fatalf("writeMotd: %v", err)
	}
	b, err := os.ReadFile(motdPath)
	if err != nil {
		t.Fatalf("read motd: %v", err)
	}
	if msg := string(b); !strings.Contains(msg, "managed by rocc") || !strings.Contains(msg, "rocc help") {
		t.Fatalf("motd should mention rocc and rocc help, got %q", msg)
	}

	// existing content is preserved, banner appended below it
	motdPath = filepath.Join(dir, "motd2")
	if err := os.WriteFile(motdPath, []byte("*NOTICE* authorized access only"), 0o644); err != nil {
		t.Fatalf("seed motd: %v", err)
	}
	if err := writeMotd(); err != nil {
		t.Fatalf("writeMotd: %v", err)
	}
	b, _ = os.ReadFile(motdPath)
	if msg := string(b); !strings.HasPrefix(msg, "*NOTICE* authorized access only") || !strings.Contains(msg, "rocc help") {
		t.Fatalf("existing motd should be kept with the banner appended, got %q", msg)
	}

	// idempotent: a second write must not duplicate the banner
	if err := writeMotd(); err != nil {
		t.Fatalf("writeMotd: %v", err)
	}
	b, _ = os.ReadFile(motdPath)
	if n := strings.Count(string(b), "managed by rocc"); n != 1 {
		t.Fatalf("banner should appear exactly once, got %d in %q", n, string(b))
	}
}

func TestValidPubKey(t *testing.T) {
	good := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB8x user@host",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQ test",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNh test",
		"sk-ssh-ed25519@openssh.com AAAA key",
	}
	for _, k := range good {
		if !validPubKey(k) {
			t.Errorf("want valid: %q", k)
		}
	}
	bad := []string{"", "garbage", "ssh-ed25519", "AAAA key", "some comment"}
	for _, k := range bad {
		if validPubKey(k) {
			t.Errorf("want invalid: %q", k)
		}
	}
}

func TestKeyFingerprint(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEDWif5DQHdS/VxaPqAeEs2qpvDvjMJqGtBK5KueGY3E shinon@shion"
	want := "SHA256:BccVFLn+hwCNfjPkgVft5Gw5k91EW8+ZFRpVG9t6qlY"
	if got := keyFingerprint(key); got != want {
		t.Errorf("fingerprint: got %s, want %s", got, want)
	}
	for _, bad := range []string{"", "garbage", "ssh-ed25519 not-base64 x", "ssh-ed25519"} {
		if got := keyFingerprint(bad); got != "" {
			t.Errorf("fingerprint of %q should be empty, got %s", bad, got)
		}
	}
}

func TestDiscoverKeys(t *testing.T) {
	key1 := "ssh-ed25519 AAAAone a@b"
	key2 := "ssh-ed25519 AAAtwo c@d"
	fileKey := "ssh-ed25519 AAAAfile f@g"
	dup := "ssh-ed25519 AAAdup x@y"
	hostKey1 := "ssh-ed25519 AAAAhost host.example"
	hostKey2 := "ssh-ed25519 AAAAhost2 host2.example"

	// path 1: key as a full string, any variable name
	t.Setenv("MY_ARBITRARY_VAR", key1)
	t.Setenv("ANOTHER_ONE", key2+"\ngarbage line")
	t.Setenv("DUP_A", dup)
	t.Setenv("DUP_B", dup)

	// path 2: value is a path to a file holding keys
	keyFile := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(keyFile, []byte(fileKey+"\nnot a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOGIN_KEYS_FILE", keyFile)

	// a directory is not a key file
	t.Setenv("POINTS_AT_DIR", filepath.Dir(keyFile))

	// host-key-shaped values must never be authorized as login keys
	t.Setenv("SSH_KNOWN_HOSTS", hostKey1)
	t.Setenv("SERVER_HOST_KEY", hostKey2)
	hostKeyFile := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(hostKeyFile, []byte(hostKey2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_KNOWN_HOSTS_FILE", hostKeyFile)

	// noise that must be ignored
	t.Setenv("NOT_A_KEY", "just some words here")
	t.Setenv("ALSO_NOT", "/this/path/does/not/exist")

	got := make(map[string]int)
	for _, k := range DiscoverKeys() {
		got[k]++
	}
	for _, want := range []string{key1, key2, fileKey, dup} {
		if got[want] == 0 {
			t.Errorf("expected key to be discovered: %q", want)
		}
	}
	if got[dup] > 1 {
		t.Error("duplicate key not deduplicated")
	}
	for _, bad := range []string{hostKey1, hostKey2} {
		if got[bad] > 0 {
			t.Errorf("host key leaked into authorized set: %q", bad)
		}
	}
}

func TestMergeAuthorizedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	baked := "ssh-ed25519 AAAAbaked platform@gateway"
	env1 := "ssh-ed25519 AAAAenv1 a@b"
	env2 := "ssh-ed25519 AAAAenv2 c@d"

	// image-baked keys and comment lines must survive a rocc boot
	if err := os.WriteFile(path, []byte("# platform-managed\n"+baked+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeAuthorizedKeys(path, []string{env1, env2, baked}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{baked, env1, env2, "# platform-managed"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("merge lost %q: %q", want, string(got))
		}
	}
	if n := strings.Count(string(got), baked); n != 1 {
		t.Errorf("baked key should appear exactly once, got %d in %q", n, string(got))
	}

	// idempotent: a second boot must not duplicate anything
	if err := mergeAuthorizedKeys(path, []string{env1, env2, baked}); err != nil {
		t.Fatalf("merge again: %v", err)
	}
	got2, _ := os.ReadFile(path)
	if string(got2) != string(got) {
		t.Errorf("second merge changed content:\n%q\n%q", string(got), string(got2))
	}

	// fresh file: just the discovered keys
	path2 := filepath.Join(t.TempDir(), "authorized_keys")
	if err := mergeAuthorizedKeys(path2, []string{env1}); err != nil {
		t.Fatalf("fresh merge: %v", err)
	}
	got3, _ := os.ReadFile(path2)
	if string(got3) != env1+"\n" {
		t.Errorf("fresh merge should hold just the key, got %q", string(got3))
	}
}
