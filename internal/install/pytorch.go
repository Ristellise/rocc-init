// PyTorch variant discovery: the recipe reads the live wheel index at
// https://download.pytorch.org/whl/ (a plain pypi HTML index: one directory
// per variant — cu118, cu126, cu130, rocm6.3, cpu, ...) instead of
// hardcoding which variant fits. The hardware-matched default is
// preselected; on an interactive terminal the user picks from a list.
package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"rocc/internal/gpu"
	"rocc/internal/proc"
	"rocc/internal/util"
)

const whlBase = "https://download.pytorch.org/whl/"

// variant is one wheel index under whlBase.
type variant struct {
	name   string // "cpu", "cu126", "rocm6.3", "xpu"
	family string // "cpu", "cuda", "rocm", "xpu"
	num    int    // 126 for cu126, 63 for rocm6.3, 0 for cpu/xpu
	url    string
}

func makeVariant(name string) variant {
	v := variant{name: name, url: whlBase + name + "/"}
	switch {
	case name == "cpu":
		v.family = "cpu"
	case strings.HasPrefix(name, "cu"):
		v.family = "cuda"
		v.num, _ = strconv.Atoi(strings.TrimPrefix(name, "cu"))
	case strings.HasPrefix(name, "rocm"):
		v.family = "rocm"
		parts := strings.Split(strings.TrimPrefix(name, "rocm"), ".")
		major, _ := strconv.Atoi(parts[0])
		minor := 0
		if len(parts) > 1 {
			minor, _ = strconv.Atoi(parts[1])
		}
		v.num = major*100 + minor // rocm6.4 -> 604, rocm7.2 -> 702
	case strings.HasPrefix(name, "xpu"):
		v.family = "xpu"
	}
	return v
}

// cudaMajor is the CUDA major a cuXXX variant belongs to (cu126 -> 12).
func (v variant) cudaMajor() int { return v.num / 10 }

var variantRe = regexp.MustCompile(`href="((?:cpu|cu[0-9]+|rocm[0-9]+(?:\.[0-9]+)*|xpu[0-9]*)/)"`)

// parseVariants extracts the variant list from the wheel index HTML.
func parseVariants(body string) []variant {
	seen := make(map[string]bool)
	var out []variant
	for _, m := range variantRe.FindAllStringSubmatch(body, -1) {
		name := strings.TrimSuffix(m[1], "/")
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, makeVariant(name))
	}
	sort.Slice(out, func(i, j int) bool { return variantLess(out[i], out[j]) })
	return out
}

func familyRank(v variant) int {
	switch v.family {
	case "cpu":
		return 0
	case "cuda":
		return 1
	case "rocm":
		return 2
	default:
		return 3
	}
}

func variantLess(a, b variant) bool {
	if ra, rb := familyRank(a), familyRank(b); ra != rb {
		return ra < rb
	}
	return a.num < b.num
}

// fetchVariants reads the live index via curl. A nil return (no curl, network
// down, index moved) is not an error: callers fall back to the static
// hardware-matched default.
func fetchVariants() []variant {
	curl, err := exec.LookPath("curl")
	if err != nil {
		return nil
	}
	out, err := proc.RunCapture(curl, "-fsSL", whlBase)
	if err != nil || out == "" {
		return nil
	}
	return parseVariants(out)
}

// maxCUDAMajorFromDriver maps an nvidia driver version to the newest CUDA
// major it supports: driver >= 580 -> 13, >= 525 -> 12, >= 450 -> 11,
// >= 410 -> 10. 0 means unknown.
func maxCUDAMajorFromDriver(driver string) int {
	maj, err := strconv.Atoi(strings.SplitN(driver, ".", 2)[0])
	if err != nil {
		return 0
	}
	switch {
	case maj >= 580:
		return 13
	case maj >= 525:
		return 12
	case maj >= 450:
		return 11
	case maj >= 410:
		return 10
	default:
		return 0
	}
}

// defaultVariant picks the hardware-matched variant from the live list.
// The default follows the device files, never cpu: nvidia -> newest cuXXX the
// driver supports, rocm -> newest rocmX.Y, render nodes only -> xpu. cpu is
// strictly a fallback for "no accelerator devices" or "family not in index".
func defaultVariant(vs []variant, g gpu.Info, driver string) (variant, bool) {
	switch g.Vendor() {
	case "nvidia":
		max := maxCUDAMajorFromDriver(driver)
		if max == 0 {
			max = 12 // unknown driver: CUDA 12.x wheels run on >= 525
		}
		if best, ok := bestInFamily(vs, "cuda", max); ok {
			return best, true
		}
	case "rocm":
		if runtime.GOARCH == "amd64" {
			if best, ok := bestInFamily(vs, "rocm", 1<<30); ok {
				return best, true
			}
		}
	case "drm":
		// render nodes with no nvidia*/kfd: most likely an intel gpu.
		if best, ok := bestInFamily(vs, "xpu", 1<<30); ok {
			return best, true
		}
	}
	return bestInFamily(vs, "cpu", 1<<30)
}

func bestInFamily(vs []variant, family string, maxMajor int) (variant, bool) {
	var best variant
	found := false
	for _, v := range vs {
		if v.family != family || v.cudaMajor() > maxMajor {
			continue
		}
		if !found || v.num > best.num {
			best, found = v, true
		}
	}
	return best, found
}

// chooseIndex resolves the wheel index for the pytorch recipe: an explicit
// override wins, else the hardware-matched default from the live index —
// confirmed interactively when we have a terminal.
func chooseIndex(g gpu.Info, override string) string {
	if override != "" {
		return override
	}
	vs := fetchVariants()
	if len(vs) == 0 {
		def := g.TorchIndex()
		util.Logf("install: pytorch: variant index unreachable, defaulting to %s", def)
		return def
	}
	driver := gpu.NvidiaDriverVersion()
	def, _ := defaultVariant(vs, g, driver)
	if !isInteractive() {
		util.Logf("install: pytorch: variant %s (driver %s)", def.name, orUnknown(driver))
		return def.url
	}
	return menu(vs, def, g, driver, bufio.NewReader(os.Stdin), os.Stdout)
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fo, err := os.Stdout.Stat()
	if err != nil || fo.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return true
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// menu shows the variant list and asks. Incompatible variants are annotated
// (the "greyed out" ones) but stay selectable as an expert override.
// EOF or empty input takes the default.
func menu(vs []variant, def variant, g gpu.Info, driver string, r *bufio.Reader, w io.Writer) string {
	fmt.Fprintf(w, "pytorch wheel variants (%s):\n\n", whlBase)
	for i, v := range vs {
		mark := "   "
		if v.name == def.name {
			mark = "-> "
		}
		if note := variantNote(v, g, driver); note != "" {
			fmt.Fprintf(w, " %s %d) %-10s (%s)\n", mark, i+1, v.name, note)
		} else {
			fmt.Fprintf(w, " %s %d) %-10s\n", mark, i+1, v.name)
		}
	}
	for {
		fmt.Fprintf(w, "\nvariant [default %s]: ", def.name)
		line, err := r.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return def.url // EOF: take the default
		}
		switch ans := strings.TrimSpace(line); {
		case ans == "":
			return def.url
		case isInt(ans):
			if n, _ := strconv.Atoi(ans); n >= 1 && n <= len(vs) {
				return vs[n-1].url
			}
		default:
			for _, v := range vs {
				if v.name == ans {
					return v.url
				}
			}
		}
		fmt.Fprintln(w, "pick a number or variant name, or press enter for the default")
	}
}

// variantNote explains why a variant may not suit this container. Empty means
// no caveat detected.
func variantNote(v variant, g gpu.Info, driver string) string {
	switch v.family {
	case "cuda":
		if g.Vendor() != "nvidia" {
			return "no nvidia devices detected"
		}
		if max := maxCUDAMajorFromDriver(driver); max > 0 && v.cudaMajor() > max {
			return fmt.Sprintf("needs CUDA %d driver, detected %s", v.cudaMajor(), driver)
		}
	case "rocm":
		if runtime.GOARCH != "amd64" {
			return "x86_64 wheels only"
		}
		if g.Vendor() != "rocm" {
			return "no amd devices detected"
		}
	case "xpu":
		if g.Vendor() == "cpu" {
			return "no gpu devices detected"
		}
	case "cpu":
		if vendor := g.Vendor(); vendor != "cpu" {
			return "cpu fallback (gpu detected: " + vendor + ")"
		}
	}
	return ""
}

func isInt(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}
