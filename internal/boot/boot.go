// Package boot is rocc's PID-1 flow: bring up sshd if keys are discovered,
// then run (and supervise) the main command. Nothing else happens at boot;
// installs are an explicit `rocc install` away, never a boot side effect.
package boot

import (
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"rocc/internal/gpu"
	"rocc/internal/proc"
	"rocc/internal/ssh"
	"rocc/internal/util"
)

// Run is the init entry point.
func Run(cmdArgs []string, version string) {
	util.Logf("rocc %s starting (pid %d)", version, os.Getpid())
	if os.Getpid() != 1 {
		util.Logf("note: not pid 1; only reaping our own children")
	}
	proc.StartReaper()

	g := gpu.Detect()
	util.Logf("gpu: %s", g.Describe())

	startSSHAutomatically()

	mainProc := startMain(cmdArgs)
	if mainProc == nil && !ssh.Running() {
		// Nothing to supervise: no sshd, no main. Better to die loudly than
		// sit as a bricked container.
		util.Logf("fatal: main command failed to start and sshd is not running; nothing to supervise")
		os.Exit(127)
	}

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	for sig := range sigCh {
		switch sig {
		case syscall.SIGTERM, syscall.SIGINT:
			util.Logf("received %s, shutting down", sig)
			ssh.Stop()
			if mainProc != nil {
				mainProc.Signal(syscall.SIGTERM)
				go func() {
					time.Sleep(10 * time.Second)
					mainProc.Signal(syscall.SIGKILL)
				}()
			} else {
				os.Exit(0)
			}
		case syscall.SIGHUP:
			util.Logf("received SIGHUP, restarting sshd")
			ssh.Restart()
		}
	}
}

// startSSHAutomatically brings up sshd when public keys were discovered in
// the environment. ssh is best-effort at boot: a failure is logged and the
// box still comes up.
func startSSHAutomatically() {
	keys := ssh.DiscoverKeys()
	if len(keys) == 0 {
		util.Logf("ssh: no public keys discovered in environment, skipping sshd (value can be a key or a path to a key file)")
		return
	}
	if err := ssh.StartSSH(keys); err != nil {
		util.Logf("warning: ssh: %v (continuing boot)", err)
	}
}

func startMain(cmdArgs []string) *proc.Proc {
	argv := mainArgv(cmdArgs)
	p, err := proc.SpawnTracked(argv)
	if err != nil {
		util.Logf("main: failed to start %q: %v", strings.Join(argv, " "), err)
		return nil
	}
	util.Logf("main: %s", strings.Join(argv, " "))
	go func() {
		code := p.Wait()
		util.Logf("main: exited with code %d", code)
		os.Exit(code)
	}()
	return p
}

// mainArgv decides what the container's "main" process is: explicit args,
// or an idle sleep so the box stays up for ssh.
func mainArgv(cmdArgs []string) []string {
	if len(cmdArgs) > 0 {
		return cmdArgs
	}
	return []string{"sleep", "infinity"}
}
