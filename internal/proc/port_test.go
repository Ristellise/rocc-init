package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fixtureProc writes a fake /proc tree: pid 747 (sshd) LISTENs on port 22
// (inode 4242), pid 800 holds an ESTABLISHED socket on port 22 (inode 5555),
// and something unrelated LISTENs on 57875 (inode 99).
func fixtureProc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt  uid  timeout inode\n" +
		"   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4242 1 0000000000000000 100 0 0 10 0\n" +
		"   1: 0100007F:0016 0100007F:89AB 01 00000000:00000000 00:00000000 00000000     0        0 5555 1 0000000000000000 20 0 0 10 0\n" +
		"   2: 00000000:E213 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0   99 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(dir, "net", "tcp"), []byte(tcp), 0o644); err != nil {
		t.Fatal(err)
	}

	for pid, ino := range map[int]string{747: "4242", 800: "5555"} {
		fds := filepath.Join(dir, strconv.Itoa(pid), "fd")
		if err := os.MkdirAll(fds, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("socket:["+ino+"]", filepath.Join(fds, "3")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "747", "cmdline"), []byte("/usr/sbin/sshd\x00-D\x00-e"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "800", "cmdline"), []byte("sshd: root@pts/0"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPortHeld(t *testing.T) {
	orig := procRoot
	procRoot = fixtureProc(t)
	defer func() { procRoot = orig }()

	pid, cmd, ok := PortHeld(22)
	if !ok {
		t.Fatal("port 22 should be held by the listener")
	}
	if pid != 747 || cmd != "/usr/sbin/sshd" {
		t.Fatalf("want pid 747 (/usr/sbin/sshd), got %d (%s)", pid, cmd)
	}
}

func TestPortHeldFree(t *testing.T) {
	orig := procRoot
	procRoot = fixtureProc(t)
	defer func() { procRoot = orig }()

	if _, _, ok := PortHeld(57875); ok {
		t.Fatal("port 57875 is held by an orphan socket, but no pid owns it")
	}
	if _, _, ok := PortHeld(9999); ok {
		t.Fatal("port 9999 should be free")
	}
}
