package install

import (
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
