// Package install implements rocc's explicit install recipes: uv, pytorch
// and package-manager passthroughs. Nothing here runs at boot; the user
// asks for each install via `rocc install ...`.
package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"rocc/internal/gpu"
	"rocc/internal/proc"
	"rocc/internal/util"
)

var uvPath string

// PyTorchOptions carries the explicit overrides for the pytorch recipe,
// from `rocc install pytorch --index ... --pkgs ...`.
type PyTorchOptions struct {
	Index string // wheel index override; empty = matched to /dev hardware
	Pkgs  string // comma/space separated override of the default package set
}

// Packages installs the given recipes, in order:
//
//	uv         the astral uv python package manager
//	pytorch    torch (+ friends) via uv, from a hardware-matched wheel index
//	apt:<pkgs> / apk:<pkgs> / pip:<pkgs>  package-manager passthroughs
func Packages(items []string, g gpu.Info, pt PyTorchOptions) error {
	for _, item := range items {
		if item == "" {
			continue
		}
		var err error
		switch item {
		case "uv":
			util.Logf("install: uv")
			err = EnsureUV()
		case "pytorch":
			util.Logf("install: pytorch (accelerator: %s)", g.Vendor())
			err = ensurePyTorch(g, pt)
		default:
			err = installPassthrough(item)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", item, err)
		}
	}
	return nil
}

func installPassthrough(item string) error {
	kind, rest, ok := strings.Cut(item, ":")
	if !ok {
		return fmt.Errorf("unknown recipe (want uv|pytorch|apt:<pkg>|apk:<pkg>|pip:<pkg>)")
	}
	pkgs := strings.Fields(strings.ReplaceAll(rest, ",", " "))
	if len(pkgs) == 0 {
		return fmt.Errorf("no packages given")
	}
	switch kind {
	case "apt":
		return proc.Run("apt-get", append([]string{"install", "-y", "--no-install-recommends"}, pkgs...)...)
	case "apk":
		return proc.Run("apk", append([]string{"add", "--no-cache"}, pkgs...)...)
	case "pip":
		return pipInstall(pkgs...)
	default:
		return fmt.Errorf("unknown recipe prefix %q (want apt|apk|pip)", kind)
	}
}

// --- uv -----------------------------------------------------------------------

func EnsureUV() error {
	if uvPath != "" {
		return nil
	}
	if p, err := exec.LookPath("uv"); err == nil {
		uvPath = p
		return nil
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	bin := filepath.Join(home, ".local", "bin", "uv")
	if _, err := os.Stat(bin); err != nil {
		if err := installUV(); err != nil {
			return err
		}
		if p, err := exec.LookPath("uv"); err == nil {
			uvPath = p
			return nil
		}
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("uv binary not found after install")
	}
	uvPath = bin
	// make uv reachable for everything we (and ssh sessions) run later
	_ = os.Setenv("PATH", os.Getenv("PATH")+":"+filepath.Dir(bin))
	_ = os.Symlink(bin, "/usr/local/bin/uv")
	return nil
}

func installUV() error {
	if _, err := exec.LookPath("curl"); err == nil {
		util.Logf("install: uv via astral.sh")
		return proc.RunScript("curl -fsSL https://astral.sh/uv/install.sh | sh")
	}
	util.Logf("install: uv via pip (no curl in image)")
	return pipInstall("uv")
}

// --- pytorch --------------------------------------------------------------------

func ensurePyTorch(g gpu.Info, opts PyTorchOptions) error {
	if err := EnsureUV(); err != nil {
		return err
	}
	pkgs := util.SplitPkgList(opts.Pkgs)
	if len(pkgs) == 0 {
		pkgs = []string{"torch", "torchvision", "torchaudio"}
	}
	index := chooseIndex(g, opts.Index, pkgs)

	// The pytorch recipe installs into an interpreter that is already there.
	// If the image has none, the user composes one in first (apt:python3,
	// apk:python3, ...) or picks a python base image; we do not download
	// interpreters on their behalf.
	py, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("no python3 in image; use a python base image or compose one in, e.g. rocc install apt:python3 && rocc install pytorch")
	}

	args := []string{"pip", "install", "--system", "--python", py, "--index-url", index}
	args = append(args, pkgs...)
	util.Logf("install: uv %s (index: %s)", strings.Join(pkgs, " "), index)
	return uvRun(args...)
}

func uvRun(args ...string) error {
	if uvPath == "" {
		if p, err := exec.LookPath("uv"); err == nil {
			uvPath = p
		}
	}
	if uvPath == "" {
		return fmt.Errorf("uv not available")
	}
	return proc.Run(uvPath, args...)
}

// --- helpers --------------------------------------------------------------------

func pipInstall(pkgs ...string) error {
	if len(pkgs) == 0 {
		return nil
	}
	if err := proc.Run("python3", append([]string{"-m", "pip", "install"}, pkgs...)...); err == nil {
		return nil
	}
	// newer pip refuses system installs without this flag (PEP 668)
	return proc.Run("python3", append([]string{"-m", "pip", "install", "--break-system-packages"}, pkgs...)...)
}
