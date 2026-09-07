package install

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"rocc/internal/gpu"
)

const fixtureIndex = `<html><head><title>Simple index</title></head><body>
<a href="cpu/">cpu/</a><br/>
<a href="cpu-pypi-pkg/">cpu-pypi-pkg/</a><br/>
<a href="cu118/">cu118/</a><br/>
<a href="cu126/">cu126/</a><br/>
<a href="cu126-full/">cu126-full/</a><br/>
<a href="cu130/">cu130/</a><br/>
<a href="cu132/">cu132/</a><br/>
<a href="nvidia/">nvidia/</a><br/>
<a href="rocm5.7/">rocm5.7/</a><br/>
<a href="rocm6.2.4/">rocm6.2.4/</a><br/>
<a href="rocm6.3/">rocm6.3/</a><br/>
<a href="rocm7.2/">rocm7.2/</a><br/>
<a href="test/">test/</a><br/>
<a href="torch/">torch/</a><br/>
<a href="xpu/">xpu/</a><br/>
</body></html>`

func TestParseVariants(t *testing.T) {
	vs := parseVariants(fixtureIndex)
	var got []string
	for _, v := range vs {
		got = append(got, v.name)
	}
	want := []string{"cpu", "cu118", "cu126", "cu130", "cu132", "rocm5.7", "rocm6.2.4", "rocm6.3", "rocm7.2", "xpu"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if vs[2].url != whlBase+"cu126/" || vs[2].num != 126 {
		t.Fatalf("cu126 variant malformed: %+v", vs[2])
	}
}

func TestDefaultVariant(t *testing.T) {
	vs := parseVariants(fixtureIndex)
	cases := []struct {
		name   string
		g      gpu.Info
		driver string
		want   string
	}{
		{"nvidia new driver", gpu.Info{NVIDIA: true}, "590.42", "cu132"},
		{"nvidia mid driver", gpu.Info{NVIDIA: true}, "550.54", "cu126"},
		{"nvidia min 12x driver", gpu.Info{NVIDIA: true}, "525.60.13", "cu126"},
		{"nvidia unknown driver", gpu.Info{NVIDIA: true}, "", "cu126"},
		{"nvidia old driver", gpu.Info{NVIDIA: true}, "450.80.02", "cu118"},
		{"nvidia pre-cu118 driver", gpu.Info{NVIDIA: true}, "440.31", "cpu"},
		{"rocm", gpu.Info{ROCm: true}, "", "rocm7.2"},
		{"drm prefers xpu over cpu", gpu.Info{DRM: true}, "", "xpu"},
		{"cpu", gpu.Info{}, "", "cpu"},
	}
	for _, c := range cases {
		want := c.want
		if c.g.ROCm && runtime.GOARCH != "amd64" {
			want = "cpu" // rocm wheels are x86_64-only
		}
		got, ok := defaultVariant(vs, c.g, c.driver)
		if !ok || got.name != want {
			t.Errorf("%s: got %q want %q", c.name, got.name, want)
		}
	}
}

func TestMaxCUDAMajorFromDriver(t *testing.T) {
	cases := map[string]int{
		"590.42": 13, "580.65": 13,
		"550.54": 12, "525.60.13": 12,
		"450.80.02": 11,
		"440.31": 10,
		"":       0, "garbage": 0,
	}
	for in, want := range cases {
		if got := maxCUDAMajorFromDriver(in); got != want {
			t.Errorf("maxCUDAMajorFromDriver(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMenuList(t *testing.T) {
	vs := parseVariants(fixtureIndex)
	names := func(g gpu.Info, driver string) []string {
		var out []string
		for _, v := range menuList(vs, g, driver) {
			out = append(out, v.name)
		}
		return out
	}

	wantEq := func(what string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", what, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v want %v", what, got, want)
			}
		}
	}

	// newest cuda first (driver allows 13), cpu fallback last
	wantEq("nvidia", names(gpu.Info{NVIDIA: true}, "590.42"),
		[]string{"cu132", "cu130", "cu126", "cu118", "cpu"})
	// driver caps at 12: older builds only
	wantEq("nvidia old driver", names(gpu.Info{NVIDIA: true}, "550.54"),
		[]string{"cu126", "cu118", "cpu"})
	// rocm: newest-first; expectation is arch-dependent
	if runtime.GOARCH == "amd64" {
		wantEq("rocm", names(gpu.Info{ROCm: true}, ""),
			[]string{"rocm7.2", "rocm6.3", "rocm6.2.4", "rocm5.7", "cpu"})
	} else {
		wantEq("rocm non-amd64", names(gpu.Info{ROCm: true}, ""), []string{"cpu"})
	}
	wantEq("drm", names(gpu.Info{DRM: true}, ""), []string{"xpu", "cpu"})
	wantEq("cpu", names(gpu.Info{}, ""), []string{"cpu"})
}

func TestPageHasWheel(t *testing.T) {
	page := `<a href="../../whl/rocm7.14/torchaudio-2.2.0-cp312-cp312-manylinux_2_28_aarch64.whl#sha256=abc">x</a>` +
		`<a href="../../whl/rocm7.14/torchaudio-2.1.2-cp311-cp311-manylinux_2_28_x86_64.whl#sha256=xyz">z</a>`
	if pageHasWheel(page, "cp312", "x86_64") {
		t.Fatal("no cp312 x86_64 wheel on that page")
	}
	if !pageHasWheel(page, "cp312", "aarch64") {
		t.Fatal("cp312 aarch64 wheel present")
	}
	if !pageHasWheel(page, "", "x86_64") {
		t.Fatal("arch-only match should work")
	}
	if pageHasWheel(page, "cp313", "aarch64") {
		t.Fatal("no cp313 wheels on that page")
	}
	good := `<a href="../../whl/rocm6.3/torchaudio-2.9.0-cp312-cp312-manylinux_2_28_x86_64.whl#sha256=def">y</a>`
	if !pageHasWheel(good, "cp312", "x86_64") {
		t.Fatal("cp312 x86_64 wheel present")
	}
}

// TestLiveValidatedDefault checks the real index: rocm7.14 (newest rocm dir
// when this was written) ships no cp312 x86_64 torchaudio, so the verified
// default on an amd64 rocm box must walk older. Skipped without curl.
func TestLiveValidatedDefault(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("needs curl for the live index")
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("live expectation is amd64-specific")
	}
	vs := fetchVariants()
	if len(vs) == 0 {
		t.Skip("variant index unreachable")
	}
	cp := cpTag()
	if cp == "" {
		t.Skip("no python3 to determine the ABI tag")
	}
	pkgs := []string{"torch", "torchvision", "torchaudio"}
	def, rejected := validatedDefault(vs, gpu.Info{ROCm: true}, "", cp, pkgs)
	if def.family != "rocm" {
		t.Fatalf("want a rocm default, got %s", def.name)
	}
	if !servesPkgs(def, pkgs, cp) {
		t.Fatalf("default %s does not serve the package set", def.name)
	}
	if len(rejected) == 0 {
		t.Log("nothing rejected; the rocm7.14 gap may have been fixed upstream")
	}
}

func TestVariantNote(t *testing.T) {
	vs := parseVariants(fixtureIndex)
	byName := map[string]variant{}
	for _, v := range vs {
		byName[v.name] = v
	}
	nvidia := gpu.Info{NVIDIA: true}

	if n := variantNote(byName["cu130"], nvidia, "550.54"); !strings.Contains(n, "CUDA 13") {
		t.Errorf("cu130 on 550.54 should note CUDA 13 requirement, got %q", n)
	}
	if n := variantNote(byName["cu130"], nvidia, "590.42"); n != "" {
		t.Errorf("cu130 on 590.42 has no caveat, got %q", n)
	}
	if n := variantNote(byName["cu126"], gpu.Info{}, "550.54"); !strings.Contains(n, "no nvidia") {
		t.Errorf("cu126 without nvidia devices should be annotated, got %q", n)
	}
	if n := variantNote(byName["xpu"], gpu.Info{DRM: true}, ""); n != "" {
		t.Errorf("xpu on drm devices is the default, no caveat expected, got %q", n)
	}
	if n := variantNote(byName["xpu"], gpu.Info{}, ""); !strings.Contains(n, "no gpu devices") {
		t.Errorf("xpu with no devices should be annotated, got %q", n)
	}
	if n := variantNote(byName["cpu"], gpu.Info{NVIDIA: true}, "590"); !strings.Contains(n, "cpu fallback") {
		t.Errorf("cpu with a gpu detected should read as fallback, got %q", n)
	}
	if n := variantNote(byName["cpu"], gpu.Info{}, ""); n != "" {
		t.Errorf("cpu on a cpu-only box is the right choice, no caveat expected, got %q", n)
	}
}
