// Command rocc is a tiny PID-1 init for containers: the two percent of
// systemd a container actually needs.
//
// `rocc init` is the long-running PID 1 process: it starts sshd when public
// keys are discovered in the environment, reaps zombies, forwards signals,
// and supervises the main command. Everything else is an explicit
// subcommand: `rocc install pytorch`, `rocc gpu`, and so on.
//
// Typical use:
//
//	ENTRYPOINT ["/usr/local/bin/rocc", "init"]
//	CMD ["sleep", "infinity"]
//
// Subcommands: init, gpu, keys, install, version, help.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"rocc/internal/bait"
	"rocc/internal/boot"
	"rocc/internal/gpu"
	"rocc/internal/install"
	"rocc/internal/ssh"
	"rocc/internal/util"
	"rocc/internal/version"
)

func main() {
	args := os.Args[1:]
	if bait.IsCompileBait(os.Args[0], args) {
		fmt.Fprintln(os.Stderr, "rocc no compile. rocc only install and init.")
		os.Exit(1)
	}

	if len(args) == 0 {
		// bare `rocc` is the init
		boot.Run(nil)
		return
	}
	switch args[0] {
	case "init":
		cmdArgs := args[1:]
		if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
			cmdArgs = cmdArgs[1:]
		}
		boot.Run(cmdArgs)
	case "gpu":
		printGPUReport()
	case "keys":
		for _, k := range ssh.DiscoverKeys() {
			fmt.Println(k)
		}
	case "install":
		opts, err := parseInstallArgs(args[1:])
		if err != nil {
			util.Fatalf("%v", err)
		}
		if err := install.Packages(opts.items, gpu.Detect(), install.PyTorchOptions{
			Index: opts.index,
			Pkgs:  opts.pkgs,
		}); err != nil {
			util.Fatalf("%v", err)
		}
	case "version", "--version":
		fmt.Println("rocc " + version.Version)
	case "help", "-h", "--help":
		printUsage(os.Stdout)
	default:
		// Nothing implicit: unknown commands are an error, not a guess.
		util.Fatalf("unknown command %q; to run it under PID 1 use `rocc init -- %s ...` (see `rocc help`)", args[0], args[0])
	}
}

// installArgs is the parsed form of `rocc install ...`.
type installArgs struct {
	items []string // recipes: uv, pytorch, apt:<pkgs>, apk:<pkgs>, pip:<pkgs>
	index string   // pytorch wheel index override
	pkgs  string   // pytorch package set override
}

func parseInstallArgs(args []string) (installArgs, error) {
	var out installArgs
	need := "" // flag awaiting its value
	for _, a := range args {
		switch need {
		case "--index":
			out.index, need = a, ""
		case "--pkgs":
			out.pkgs, need = a, ""
		default:
			switch {
			case a == "--index" || a == "--pkgs":
				need = a
			case strings.HasPrefix(a, "--index="):
				out.index = strings.TrimPrefix(a, "--index=")
			case strings.HasPrefix(a, "--pkgs="):
				out.pkgs = strings.TrimPrefix(a, "--pkgs=")
			default:
				out.items = append(out.items, a)
			}
		}
	}
	if need != "" {
		return out, fmt.Errorf("%s requires a value", need)
	}
	if len(out.items) == 0 {
		return out, fmt.Errorf("nothing to install (recipes: uv pytorch apt:<pkg> apk:<pkg> pip:<pkg>)")
	}
	return out, nil
}

func printGPUReport() {
	g := gpu.Detect()
	report := struct {
		Vendor     string   `json:"vendor"`
		NVIDIA     bool     `json:"nvidia"`
		ROCm       bool     `json:"rocm"`
		DRM        bool     `json:"drm"`
		Devices    []string `json:"devices"`
		TorchIndex string   `json:"torch_index"`
	}{
		Vendor:     g.Vendor(),
		NVIDIA:     g.NVIDIA,
		ROCm:       g.ROCm,
		DRM:        g.DRM,
		Devices:    g.Devices,
		TorchIndex: g.TorchIndex(),
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		util.Fatalf("%v", err)
	}
	fmt.Println(string(b))
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `rocc `+version.Version+` - tiny PID 1 init for containers

Usage:
  rocc init [cmd args...]   run as PID 1: sshd if keys are discovered, supervise cmd (default: sleep infinity)
  rocc init -- cmd args...   same, but never interpret cmd args
  rocc gpu                   print detected accelerators as JSON
  rocc keys                  print public keys discovered in the environment
  rocc install <item>...     install recipes now
  rocc version | help

rocc with no arguments is "rocc init".

Install recipes:
  uv                      astral uv python manager
  pytorch                 torch+vision+audio via uv, wheel index matched to /dev hardware
  apt:<pkgs> apk:<pkgs> pip:<pkgs>   package-manager passthroughs
  flags: --index <url>    pytorch wheel index override
         --pkgs <list>    pytorch package set override (e.g. "torch,torchvision")
`)
}
