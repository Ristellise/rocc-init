// Package ssh discovers public keys in the environment and runs a
// supervised, public-key-only sshd as root on port 22. ssh is the one
// service rocc runs itself, so it installs openssh-server when missing.
package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"rocc/internal/proc"
	"rocc/internal/util"
	"rocc/internal/version"
)

var sshSupervisor *proc.Supervisor

// DiscoverKeys collects SSH public keys from the environment by value, not
// by name. Every variable's value is checked two ways, in order:
//
//  1. the value itself is (or contains, comma/newline separated) a public key
//  2. the value is a path to a readable file that holds public keys
//
// So KEYS="ssh-ed25519 AAAA... me@laptop" and
// KEYS=/run/secrets/authorized_keys both work, under any variable name.
//
// Names that hold *host* keys rather than login keys (anything containing
// KNOWN_HOSTS, or *_HOST_KEY(S)) are skipped both ways so host keys never
// leak into authorized_keys. Malformed lines are dropped, duplicates
// removed. There are no network fetches: remote key sources are the
// launcher's job.
func DiscoverKeys() []string {
	env := os.Environ()
	sort.Slice(env, func(i, j int) bool {
		ni, _, _ := strings.Cut(env[i], "=")
		nj, _, _ := strings.Cut(env[j], "=")
		return ni < nj
	})

	var keys []string
	add := func(v string) {
		for _, k := range util.SplitList(v) {
			if validPubKey(k) {
				keys = append(keys, k)
			}
		}
	}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if isHostKeyName(name) {
			continue
		}
		add(value)
		add(readKeyFile(value))
	}
	seen := make(map[string]bool, len(keys))
	out := keys[:0]
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func validPubKey(line string) bool {
	f := strings.Fields(line)
	if len(f) < 2 {
		return false
	}
	for _, p := range []string{"ssh-", "ecdsa-", "sk-"} {
		if strings.HasPrefix(f[0], p) {
			return true
		}
	}
	return false
}

// isHostKeyName reports whether an env var is expected to hold ssh *host*
// keys (known hosts, server host keys) instead of login keys.
func isHostKeyName(name string) bool {
	n := strings.ToUpper(name)
	return strings.Contains(n, "KNOWN_HOSTS") ||
		n == "HOST_KEY" || n == "HOST_KEYS" ||
		strings.HasSuffix(n, "_HOST_KEY") || strings.HasSuffix(n, "_HOST_KEYS")
}

// readKeyFile returns the contents of the file at path when path names a
// readable regular file of sane size, and "" otherwise. Files that do not
// exist, are unreadable, or are huge simply are not key files; discovery is
// best-effort and never fatal.
func readKeyFile(path string) string {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// Running reports whether the sshd supervisor is up.
func Running() bool {
	return sshSupervisor != nil
}

// StartSSH brings up sshd for root on port 22: installs openssh-server if
// the binary is missing, merges authorized_keys, generates host keys, and
// starts a supervised sshd. An image that already ships sshd gets it
// untouched: rocc runs the system sshd_config (Include drop-ins included)
// and only adds keys. rocc writes its own appliance config when it had to
// install openssh-server itself.
func StartSSH(keys []string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("sshd must run as root")
	}
	bin, err := findSSHD()
	installed := false
	if err != nil {
		util.Logf("ssh: sshd not found, installing openssh-server")
		if err := ensureOpenSSH(); err != nil {
			return err
		}
		if bin, err = findSSHD(); err != nil {
			return err
		}
		installed = true
	}
	if err := os.MkdirAll("/run/sshd", 0o755); err != nil {
		return err
	}
	if err := ensureHostKeys(); err != nil {
		return err
	}
	if err := setupAuthorizedKeys(keys); err != nil {
		return err
	}
	args := []string{bin, "-D", "-e"}
	if installed {
		cfg, err := writeSSHDConfig(true)
		if err != nil {
			return err
		}
		out, testErr := proc.RunCapture(bin, "-t", "-f", cfg)
		if testErr != nil {
			if !strings.Contains(strings.ToLower(out), "pam") {
				return fmt.Errorf("sshd config check failed: %v\n%s", testErr, out)
			}
			// sshd builds without PAM support (e.g. Alpine) reject the UsePAM
			// option; fall back to a PAM-free config.
			if cfg, err = writeSSHDConfig(false); err != nil {
				return err
			}
			if out, testErr = proc.RunCapture(bin, "-t", "-f", cfg); testErr != nil {
				return fmt.Errorf("sshd config check failed: %v\n%s", testErr, out)
			}
			util.Logf("ssh: sshd built without PAM, using PAM-free config")
		}
		args = append(args, "-f", cfg)
		util.Logf("ssh: %d key(s) authorized for root, sshd on port 22", len(keys))
	} else {
		if out, err := proc.RunCapture(bin, "-t"); err != nil {
			return fmt.Errorf("system sshd_config check failed: %v\n%s", err, out)
		}
		util.Logf("ssh: %d key(s) authorized for root, using system sshd_config", len(keys))
	}
	if err := writeMotd(); err != nil {
		util.Logf("ssh: motd: %v (continuing)", err)
	}

	s := &proc.Supervisor{Name: "sshd"}
	s.Spawn = func() *proc.Proc {
		// If something else already listens on 22 (a distro sshd started
		// by the image, say), spawning ours would just die with "Address
		// already in use" and churn. Wait for the port instead; the
		// supervisor backs off between attempts.
		if pid, cmd, held := proc.PortHeld(22); held {
			util.Logf("ssh: port 22 in use by pid %d (%s), waiting", pid, cmd)
			return nil
		}
		p, err := proc.SpawnDaemon(args)
		if err != nil {
			util.Logf("ssh: %v", err)
			return nil
		}
		return p
	}
	sshSupervisor = s
	go s.Loop()
	return nil
}

// Stop shuts the sshd supervisor down (used at container shutdown).
func Stop() {
	if sshSupervisor != nil {
		sshSupervisor.Stop()
	}
}

// Restart kills sshd; the supervisor brings it back with a fresh config read.
func Restart() {
	if sshSupervisor != nil {
		sshSupervisor.Signal(syscall.SIGTERM)
	}
}

func findSSHD() (string, error) {
	if p, err := exec.LookPath("sshd"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd", "/sbin/sshd"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("sshd not found")
}

// ensureOpenSSH installs openssh-server with whatever package manager the
// image has. ssh is rocc's own service, so rocc installs it itself.
func ensureOpenSSH() error {
	switch distroID() {
	case "debian", "ubuntu":
		if err := proc.Run("apt-get", "update", "-qq"); err != nil {
			return err
		}
		return proc.Run("apt-get", "install", "-y", "--no-install-recommends", "openssh-server")
	case "alpine":
		return proc.Run("apk", "add", "--no-cache", "openssh-server")
	case "fedora", "rocky", "almalinux", "centos":
		return proc.Run("dnf", "install", "-y", "openssh-server")
	}
	return fmt.Errorf("no supported package manager (apt/apk/dnf)")
}

func distroID() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			return strings.Trim(strings.ToLower(v), `"`)
		}
	}
	return ""
}

func ensureHostKeys() error {
	if m, _ := filepath.Glob("/etc/ssh/ssh_host_*_key"); len(m) > 0 {
		return nil
	}
	bin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return fmt.Errorf("no host keys in /etc/ssh and ssh-keygen not found")
	}
	return proc.Run(bin, "-A")
}

// setupAuthorizedKeys merges discovered keys into root's authorized_keys.
// Existing lines (image-baked or platform-written keys) are preserved:
// rocc never removes a key. Revoke by editing the file — sshd re-reads it
// on every login.
func setupAuthorizedKeys(keys []string) error {
	u, err := user.Lookup("root")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	dir := filepath.Join(u.HomeDir, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chown(dir, uid, gid)
	path := filepath.Join(dir, "authorized_keys")
	if err := mergeAuthorizedKeys(path, keys); err != nil {
		return err
	}
	return os.Chown(path, uid, gid)
}

// mergeAuthorizedKeys appends keys to the file at path, keeping existing
// non-empty lines and dropping duplicates. Idempotent.
func mergeAuthorizedKeys(path string, keys []string) error {
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	seen := make(map[string]bool, len(lines)+len(keys))
	for _, l := range lines {
		seen[l] = true
	}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			lines = append(lines, k)
		}
	}
	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// motdPath is overridable for tests.
var motdPath = "/etc/motd"

// writeMotd appends the login banner to /etc/motd, preserving any existing
// content (distro legal notices and the like). The rocc sshd config prints
// the motd (PrintMotd yes) on interactive logins, so a human landing in the
// box learns `rocc help` exists. Idempotent: skips writing when the banner
// is already there.
func writeMotd() error {
	msg := fmt.Sprintf(
		"this container is managed by rocc %s\n\n  rocc help    list commands and install recipes\n",
		version.Version,
	)
	switch old, err := os.ReadFile(motdPath); {
	case err == nil:
		if strings.Contains(string(old), "this container is managed by rocc") {
			return nil
		}
		content := string(old)
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		msg = content + "\n" + msg
	case os.IsNotExist(err):
		// no motd yet: banner only
	default:
		return err
	}
	return os.WriteFile(motdPath, []byte(msg), 0o644)
}

// writeSSHDConfig generates a self-contained, appliance-style config:
// public key auth only, no passwords, root, port 22.
func writeSSHDConfig(usePAM bool) (string, error) {
	var b strings.Builder
	// Distro drop-ins come first so they override rocc's settings, same
	// precedence as a stock sshd_config.
	b.WriteString("Include /etc/ssh/sshd_config.d/*.conf\n")
	b.WriteString("Port 22\n")
	b.WriteString("PermitRootLogin prohibit-password\n")
	b.WriteString(`
PubkeyAuthentication yes
PasswordAuthentication no
KbdInteractiveAuthentication no
ChallengeResponseAuthentication no
AuthorizedKeysFile .ssh/authorized_keys
StrictModes no
X11Forwarding no
PrintMotd yes
LoginGraceTime 15
MaxStartups 3:30:6
Subsystem sftp internal-sftp
LogLevel INFO
`)
	if usePAM {
		b.WriteString("UsePAM no\n")
	}
	const path = "/etc/ssh/sshd_config.rocc"
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
