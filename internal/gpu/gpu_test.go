package gpu

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDetect(t *testing.T) {
	old := devRoot
	t.Cleanup(func() { devRoot = old })

	root := t.TempDir()
	devRoot = root
	if g := Detect(); g.NVIDIA || g.ROCm || g.DRM || g.Vendor() != "cpu" {
		t.Fatalf("empty /dev should be cpu-only, got %+v", g)
	}

	root = t.TempDir()
	devRoot = root
	for _, f := range []string{"nvidia0", "nvidiactl", "nvidia-uvm"} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g := Detect()
	if !g.NVIDIA || g.Vendor() != "nvidia" {
		t.Fatalf("nvidia nodes not detected: %+v", g)
	}
	if g.TorchIndex() != "https://download.pytorch.org/whl/cu126" {
		t.Fatalf("nvidia should map to cuda wheels, got %s", g.TorchIndex())
	}

	root = t.TempDir()
	devRoot = root
	if err := os.WriteFile(filepath.Join(root, "kfd"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dri"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dri", "renderD128"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	g = Detect()
	if !g.ROCm || g.Vendor() != "rocm" {
		t.Fatalf("rocm nodes not detected: %+v", g)
	}
	want := "https://download.pytorch.org/whl/cpu"
	if runtime.GOARCH == "amd64" {
		want = "https://download.pytorch.org/whl/rocm6.3"
	}
	if g.TorchIndex() != want {
		t.Fatalf("rocm should map to %s, got %s", want, g.TorchIndex())
	}

	root = t.TempDir()
	devRoot = root
	if err := os.MkdirAll(filepath.Join(root, "dri"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dri", "card0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	g = Detect()
	if !g.DRM || g.Vendor() != "drm" || g.TorchIndex() != "https://download.pytorch.org/whl/cpu" {
		t.Fatalf("drm-only should fall back to cpu wheels, got %+v", g)
	}
}
