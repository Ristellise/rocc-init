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

// matchedFamily maps detected hardware to the wheel family it wants.
func matchedFamily(g gpu.Info) string {
	switch g.Vendor() {
	case "nvidia":
		return "cuda"
	case "rocm":
		if runtime.GOARCH == "amd64" {
			return "rocm"
		}
	case "drm":
		return "xpu"
	}
	return "cpu"
}

// familyMax is the driver-imposed ceiling for a family; 1<<30 for all but
// cuda, where the detected nvidia driver bounds the CUDA major (cu12x when
// the driver is unknown: CUDA 12.x wheels run on >= 525).
func familyMax(family, driver string) int {
	if family != "cuda" {
		return 1 << 30
	}
	if m := maxCUDAMajorFromDriver(driver); m > 0 {
		return m
	}
	return 12
}

// defaultVariant picks the hardware-matched variant from the live list.
// The default follows the device files, never cpu: nvidia -> newest cuXXX the
// driver supports, rocm -> newest rocmX.Y, render nodes only -> xpu. cpu is
// strictly a fallback for "no accelerator devices" or "family not in index".
func defaultVariant(vs []variant, g gpu.Info, driver string) (variant, bool) {
	family := matchedFamily(g)
	if best, ok := bestInFamily(vs, family, familyMax(family, driver)); ok {
		return best, true
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
// verified against the target packages, and confirmed interactively when we
// have a terminal.
func chooseIndex(g gpu.Info, override string, pkgs []string) string {
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
	def, rejected := validatedDefault(vs, g, driver, cpTag(), pkgs)
	if !isInteractive() {
		util.Logf("install: pytorch: variant %s (driver %s)", def.name, orUnknown(driver))
		return def.url
	}
	return menu(vs, def, g, driver, rejected, bufio.NewReader(os.Stdin), os.Stdout)
}

// validatedDefault walks the curated newest-first list and returns the first
// variant whose index actually serves every target package for this
// platform and python. The live index has partial dirs (rocm7.14 ships no
// cp312 x86_64 torchaudio), so newest-by-sort is not always installable.
// Also returns the names it rejected, for the menu to annotate.
func validatedDefault(vs []variant, g gpu.Info, driver, cp string, pkgs []string) (variant, map[string]bool) {
	rejected := map[string]bool{}
	for _, v := range menuList(vs, g, driver) {
		if servesPkgs(v, pkgs, cp) {
			return v, rejected
		}
		rejected[v.name] = true
	}
	def, _ := defaultVariant(vs, g, driver)
	return def, rejected
}

// servesPkgs checks that every target package has an installable wheel in
// the variant's index.
func servesPkgs(v variant, pkgs []string, cp string) bool {
	tag := archTag()
	if cp != "" {
		tag = cp + " " + tag
	}
	for _, pkg := range pkgs {
		if !servesWheels(v.url+pkg+"/", cp) {
			util.Logf("install: pytorch: %s has no %s wheels, trying older", v.name, tag)
			return false
		}
	}
	return true
}

// servesWheels reports whether url (a /whl/<variant>/<pkg>/ page) lists a
// wheel for this platform and python. Best-effort: when the page cannot be
// fetched it says yes, and the resolver reports the real problem.
func servesWheels(url, cp string) bool {
	curl, err := exec.LookPath("curl")
	if err != nil {
		return true
	}
	out, err := proc.RunCapture(curl, "-fsSL", url)
	if err != nil {
		return true
	}
	return pageHasWheel(out, cp, archTag())
}

var hrefRe = regexp.MustCompile(`href="[^"]*\.whl[^"]*"`)

// pageHasWheel reports whether an index page lists a wheel href matching
// both the python ABI tag (when known) and the arch.
func pageHasWheel(body, cp, arch string) bool {
	for _, href := range hrefRe.FindAllString(body, -1) {
		if strings.Contains(href, arch) && (cp == "" || strings.Contains(href, cp)) {
			return true
		}
	}
	return false
}

// cpTag returns the image python's ABI tag (e.g. "cp312"), or "" when
// python3 cannot answer.
func cpTag() string {
	py, err := exec.LookPath("python3")
	if err != nil {
		return ""
	}
	out, err := proc.RunCapture(py, "-c", "import sys;print(sys.implementation.cache_tag)")
	if err != nil {
		return ""
	}
	var digits []byte
	for _, c := range out {
		if c >= '0' && c <= '9' {
			digits = append(digits, byte(c))
		}
	}
	if len(digits) == 0 {
		return ""
	}
	return "cp" + string(digits)
}

func archTag() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return "x86_64"
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

// menuShow caps how many builds the interactive menu lists.
const menuShow = 5

// menuList curates the menu: the newest few builds for the detected
// hardware (gated by the driver for cuda) plus the cpu fallback. The full
// index stays reachable by typing a variant name.
func menuList(vs []variant, g gpu.Info, driver string) []variant {
	family := matchedFamily(g)
	max := familyMax(family, driver)
	var shown []variant
	for _, v := range vs {
		if v.family == family && v.cudaMajor() <= max {
			shown = append(shown, v)
		}
	}
	sort.Slice(shown, func(i, j int) bool { return shown[i].num > shown[j].num })
	if len(shown) > menuShow {
		shown = shown[:menuShow]
	}
	if family != "cpu" {
		if cpu, ok := bestInFamily(vs, "cpu", 1<<30); ok {
			shown = append(shown, cpu)
		}
	}
	return shown
}

// menu shows the curated variant list and asks. Numbers pick from the
// listed entries; any variant name from the full index works too; enter or
// EOF takes the default.
func menu(vs []variant, def variant, g gpu.Info, driver string, rejected map[string]bool, r *bufio.Reader, w io.Writer) string {
	shown := menuList(vs, g, driver)
	fmt.Fprintf(w, "pytorch wheel variants (%s):\n\n", whlBase)
	for i, v := range shown {
		mark := "   "
		if v.name == def.name {
			mark = "-> "
		}
		note := variantNote(v, g, driver)
		if rejected[v.name] {
			if note != "" {
			note += "; "
			}
			note += "no installable wheels here"
		}
		if note != "" {
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
			if n, _ := strconv.Atoi(ans); n >= 1 && n <= len(shown) {
				return shown[n-1].url
			}
		default:
			for _, v := range vs {
				if v.name == ans {
					return v.url
				}
			}
		}
		fmt.Fprintln(w, "pick a listed number, or type any variant name (e.g. rocm6.2, cu126)")
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
