package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot lets tests point the lookups below at a fixture directory.
var procRoot = "/proc"

// PortHeld reports the pid and argv0 of the process holding a LISTEN
// socket on port, reading /proc only. ok=false means no listener was found
// (or /proc was unreadable — in which case the bind call gets to judge).
func PortHeld(port int) (pid int, cmd string, ok bool) {
	inodes := listenInodes("tcp", port)
	inodes = append(inodes, listenInodes("tcp6", port)...)
	if len(inodes) == 0 {
		return 0, "", false
	}
	holders := make(map[string]bool, len(inodes))
	for _, ino := range inodes {
		holders["socket:["+ino+"]"] = true
	}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, "", false
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procRoot, e.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(procRoot, e.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			if holders[link] {
				return pid, argv0(pid), true
			}
		}
	}
	return 0, "", false
}

// listenInodes returns the socket inodes that LISTEN on port according to
// /proc/net/<name>. Columns: sl local_address rem_address st ... inode.
func listenInodes(name string, port int) []string {
	b, err := os.ReadFile(filepath.Join(procRoot, "net", name))
	if err != nil {
		return nil
	}
	want := fmt.Sprintf(":%04X", port)
	var inodes []string
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 10 || !strings.HasSuffix(f[1], want) || !strings.EqualFold(f[3], "0A") {
			continue
		}
		inodes = append(inodes, f[9])
	}
	return inodes
}

func argv0(pid int) string {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(b) == 0 {
		return "?"
	}
	if i := strings.IndexByte(string(b), 0); i > 0 {
		return string(b[:i])
	}
	return string(b)
}
