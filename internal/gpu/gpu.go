// Package gpu probes /dev for accelerator hardware. Detection is purely
// device-node based: no lspci, no nvidia-smi required to boot.
package gpu

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// devRoot is overridable so tests can fake a /dev tree.
var devRoot = "/dev"

// Info is the result of probing /dev for accelerators.
type Info struct {
	NVIDIA  bool     `json:"nvidia"`
	ROCm    bool     `json:"rocm"`
	DRM     bool     `json:"drm"` // generic render nodes (iGPUs, etc.)
	Devices []string `json:"devices"`
}

// Vendor returns the most specific accelerator detected.
func (g Info) Vendor() string {
	switch {
	case g.NVIDIA:
		return "nvidia"
	case g.ROCm:
		return "rocm"
	case g.DRM:
		return "drm"
	default:
		return "cpu"
	}
}

// TorchIndex maps the detected hardware to a PyTorch wheel index. This is
// the offline fallback used when the live index cannot be read; cpu means
// "no accelerator device nodes at all".
func (g Info) TorchIndex() string {
	switch g.Vendor() {
	case "nvidia":
		return "https://download.pytorch.org/whl/cu126"
	case "rocm":
		if runtime.GOARCH == "amd64" {
			return "https://download.pytorch.org/whl/rocm6.3"
		}
		return "https://download.pytorch.org/whl/cpu" // rocm wheels are x86_64-only
	case "drm":
		return "https://download.pytorch.org/whl/xpu" // render nodes: likely intel gpu
	default:
		return "https://download.pytorch.org/whl/cpu"
	}
}

// Describe renders a one-line human summary for the boot log.
func (g Info) Describe() string {
	var parts []string
	if g.NVIDIA {
		if v := NvidiaDriverVersion(); v != "" {
			parts = append(parts, "nvidia (driver "+v+")")
		} else {
			parts = append(parts, "nvidia")
		}
	}
	if g.ROCm {
		parts = append(parts, "rocm (/dev/kfd)")
	}
	if g.DRM {
		parts = append(parts, "drm (/dev/dri)")
	}
	if len(parts) == 0 {
		return "cpu only (no accelerator device nodes under /dev)"
	}
	return strings.Join(parts, ", ") + fmt.Sprintf(" [%d device nodes]", len(g.Devices))
}

// Detect probes /dev for accelerator device nodes. It never fails: the
// absence of nodes simply means CPU.
func Detect() Info {
	var g Info
	add := func(pat string) bool {
		m, _ := filepath.Glob(filepath.Join(devRoot, pat))
		if len(m) == 0 {
			return false
		}
		g.Devices = append(g.Devices, m...)
		return true
	}
	g.NVIDIA = add("nvidia*")
	g.ROCm = add("kfd")
	dri := add("dri/*")
	g.DRM = dri || g.ROCm // rocm cards also expose render nodes under /dev/dri
	sort.Strings(g.Devices)
	return g
}

// NvidiaDriverVersion reports the host nvidia driver via nvidia-smi when
// available; empty means unknown and that is fine.
func NvidiaDriverVersion() string {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return ""
	}
	out, err := exec.Command(bin, "--query-gpu=driver_version", "--format=csv,noheader").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
