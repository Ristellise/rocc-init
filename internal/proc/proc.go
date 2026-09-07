// Package proc is rocc-init's process core: PID-1 duties (zombie reaping,
// signal-aware children) plus a tiny supervisor for services.
package proc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"rocc/internal/util"
)

var (
	spawnMu    sync.Mutex // serializes child spawning against the reaper
	tracked    sync.Map   // pid -> chan int (exit-code delivery)
	reaperOnce sync.Once
)

// StartReaper reaps zombies. As PID 1, every orphaned process in the container
// (double-forked daemons, abandoned sshd sessions, ...) is reparented to us;
// if nobody wait()s on them they accumulate forever.
//
// The reaper is the *only* waiter: every child we care about is spawned via
// StartProc, which registers it under spawnMu before the lock is released, so
// a fast exit can never be reaped before its channel exists. StartProc
// calls StartReaper too, so subcommand invocations (`rocc install ...` from
// a shell, not as PID 1) get a reaper without anyone wiring one up.
func StartReaper() {
	reaperOnce.Do(func() {
		sigCh := make(chan os.Signal, 64)
		signal.Notify(sigCh, syscall.SIGCHLD)
		go func() {
			for range sigCh {
				reapOnce()
			}
		}()
	})
}

// reapOnce drains all currently-dead children, handing exit codes to tracked
// waiters and silently reaping untracked zombies.
func reapOnce() {
	spawnMu.Lock()
	defer spawnMu.Unlock()
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err != nil || pid <= 0 {
			return
		}
		if ch, ok := tracked.Load(pid); ok {
			tracked.Delete(pid)
			if c, ok := ch.(chan int); ok {
				c <- exitCode(ws)
			}
		}
	}
}

func exitCode(ws syscall.WaitStatus) int {
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

// Proc is a child process whose exit status is routed to us by the reaper.
type Proc struct {
	cmd *exec.Cmd
	ch  chan int
}

// Wait blocks until the process exits and returns its exit code.
func (p *Proc) Wait() int {
	return <-p.ch
}

// Signal sends sig to the process.
func (p *Proc) Signal(sig syscall.Signal) {
	_ = p.cmd.Process.Signal(sig)
}

// SpawnTracked runs argv in the foreground, inheriting our stdio.
func SpawnTracked(argv []string) (*Proc, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return StartProc(cmd)
}

// SpawnDaemon runs argv as a background service: no stdin/stdout, stderr
// inherited so its logs land in the container log.
func SpawnDaemon(argv []string) (*Proc, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	return StartProc(cmd)
}

// StartProc starts cmd and registers it with the reaper while holding
// spawnMu, closing the fork/register race window.
func StartProc(cmd *exec.Cmd) (*Proc, error) {
	spawnMu.Lock()
	defer spawnMu.Unlock()
	// Before Start: the SIGCHLD handler must exist before the child can,
	// or a fast exit could go unnoticed forever.
	StartReaper()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &Proc{cmd: cmd, ch: make(chan int, 1)}
	tracked.Store(cmd.Process.Pid, p.ch)
	return p, nil
}

// Supervisor keeps a daemon running, restarting it with capped backoff when
// it dies on its own. It is rocc-init's entire unit system.
type Supervisor struct {
	Name string

	mu      sync.Mutex
	stopped bool
	proc    *Proc
	Spawn   func() *Proc // nil return means "could not start, retry"
}

// Loop runs the supervise/restart cycle until Stop is called.
func (s *Supervisor) Loop() {
	backoff := time.Second
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		p := s.Spawn()
		s.proc = p
		s.mu.Unlock()

		if p == nil {
			time.Sleep(backoff)
			backoff = min(2*backoff, 30*time.Second)
			continue
		}

		started := time.Now()
		code := p.Wait()
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // it ran a while; not a crash loop
		}

		s.mu.Lock()
		s.proc = nil
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}
		util.Logf("%s: exited with code %d, restarting in %s", s.Name, code, backoff)
		time.Sleep(backoff)
		backoff = min(2*backoff, 30*time.Second)
	}
}

// Signal sends sig to the currently running daemon process, if any.
func (s *Supervisor) Signal(sig syscall.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil {
		s.proc.Signal(sig)
	}
}

// Stop permanently stops the supervisor and its daemon.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.stopped = true
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		p.Signal(syscall.SIGTERM)
	}
}

// Run runs a command to completion, streaming output to stderr. It goes
// through StartProc so the reaper stays the sole waiter for every child.
func Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return runCmd(cmd)
}

// RunScript runs `sh -c script` to completion, streaming output to stderr.
func RunScript(script string) error {
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return runCmd(cmd)
}

func runCmd(cmd *exec.Cmd) error {
	cmd.Env = runEnv()
	p, err := StartProc(cmd)
	if err != nil {
		return err
	}
	if code := p.Wait(); code != 0 {
		return fmt.Errorf("exit status %d", code)
	}
	return nil
}

// RunCapture runs a command to completion and returns its combined output.
func RunCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.Env = runEnv()
	p, err := StartProc(cmd)
	if err != nil {
		return "", err
	}
	if code := p.Wait(); code != 0 {
		return buf.String(), fmt.Errorf("exit status %d", code)
	}
	return buf.String(), nil
}

func runEnv() []string {
	return append(os.Environ(),
		"DEBIAN_FRONTEND=noninteractive",
		"UV_BREAK_SYSTEM_PACKAGES=1",
	)
}
