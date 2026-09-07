# rocc

A tiny, dependency-free Go binary that runs as **PID 1** in a container and
turns any base image into an SSH-accessible dev box. Boot does exactly one
job — ssh — and everything else you ask for explicitly.

It is the ~2% of systemd a container actually needs, without the other 98%:

| systemd does                          | rocc does                                 |
|---------------------------------------|-------------------------------------------|
| cgroups, targets, dependency graph    | one built-in service: sshd (with backoff) |
| dbus, journald, state in /var         | logs to stderr, zero config               |
| unit files, fifty knobs per unit      | `rocc install pytorch`                    |
| 30+ MB + libc deps                    | one static binary, stdlib only, no cgo   |

There are **no configuration environment variables**. Keys are discovered
(by value), hardware is detected (by probing /dev), and anything you want
installed you ask for explicitly.

Boot sequence:

```
rocc init (PID 1)
 ├─ probe /dev for accelerators (logged; `rocc gpu` for JSON)
 ├─ sshd  ← keys discovered in the environment (key string or path to key file)
 └─ main  ← `rocc init` args (container CMD), or `sleep infinity`
```

## Quick start: pull the binary at container start

From any base image with curl:

```sh
docker run -d --name dev -p 2222:22 \
  -e SSH_KEY="ssh-ed25519 AAAAC3... you@laptop" \
  ubuntu:24.04 \
  bash -c 'apt-get update -qq && apt-get install -y -qq curl && \
            curl -fsSL https://github.com/Ristellise/rocc-init/releases/latest/download/rocc_linux_amd64 \
              -o /usr/local/bin/rocc && \
            chmod +x /usr/local/bin/rocc && \
            exec rocc init'
```

(on arm64 hosts use `rocc_linux_arm64`; each release also carries `.sha256`
checksums, and `releases/latest/download/<name>` always resolves to the
newest release)

- sshd starts because a public key was discovered in `SSH_KEY` (any variable
  name works)
- openssh-server is installed automatically if missing — ssh is the one
  service rocc runs itself
- `ssh -p 2222 root@localhost` — public key only, no passwords, port 22,
  root

Then, from inside the box (ssh or docker exec):

```sh
rocc gpu                          # what hardware did we land on?
rocc install uv                   # astral uv
rocc install pytorch              # torch+vision+audio, wheels matched to /dev
rocc install pytorch --pkgs "torch"
rocc install pytorch --index https://download.pytorch.org/whl/cu121
rocc install apt:htop,curl
```

`rocc install pytorch` installs uv first if it is not there yet.

## Releasing

Tag a commit and push the tag; the [release workflow](.github/workflows/release.yml)
runs vet + tests, builds static binaries for linux/amd64 and linux/arm64,
stamps `rocc version` with the tag, and attaches them (plus `.sha256`
checksums) to the GitHub release:

```sh
git tag v0.4.0
git push origin v0.4.0
```

If you prefer baking a minimal image instead of pulling at start:

```dockerfile
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
COPY rocc /usr/local/bin/rocc
ENTRYPOINT ["/usr/local/bin/rocc", "init"]
# CMD ["python", "train.py"]   # optional: supervised under PID 1
```

## Key discovery: two paths, by value

Every environment variable's value is checked two ways, under any name:

1. **The value is a key.** A public key string (or several, comma/newline
   separated) gets authorized — `SSH_PUBLIC_KEY`, `CI_LOGIN_KEY`, whatever
   you already have.
2. **The value is a path.** If it points to a readable file, public keys
   inside it get authorized:

   ```sh
   -e KEYS=/run/secrets/authorized_keys
   ```

This covers `--env-file` setups, mounted secrets, and orchestrators that
inject files. Variables that hold *host* keys (`KNOWN_HOSTS`, `*_HOST_KEY`)
are skipped both ways so host keys never leak into authorized_keys.

rocc never fetches anything itself. Keys that live somewhere remote (GitHub,
GitLab, a vault) are fetched at launch time, by whatever launches the
container, and passed in via either path — discovery picks them up:

```sh
-e KEYS="$(curl -fsSL https://github.com/<user>.keys)"
```

sshd comes up **only if at least one key is discovered**, so plain
`docker run image python train.py` stays a normal container.

Interactive ssh logins get a short banner via `/etc/motd`:

```
this container is managed by rocc v0.4.0

  rocc help    list commands and install recipes
```

Appended to any existing motd when sshd starts — distro notices already in
the file are kept, never overwritten. Non-interactive sessions (`ssh host
cmd`, rsync) print nothing extra.

## Install recipes (`rocc install ...`)

- `uv` — installs the astral uv manager, symlinked to `/usr/local/bin/uv`
- `pytorch` — installs torch (+ vision/audio) via uv into the image's
  python3, from a **hardware-matched** wheel index discovered live from
  https://download.pytorch.org/whl/
- `apt:<pkgs>` / `apk:<pkgs>` / `pip:<pkgs>` — passthrough installs

Flags: `--index <url>` overrides the wheel index, `--pkgs "a,b"` overrides
the package set.

The pytorch recipe installs into an interpreter that already exists in the
image — it never downloads a python of its own. On a bare image, compose
one in first: `rocc install apt:python3 && rocc install pytorch` (or use a
python base image).

## Hardware detection (by /dev only)

| /dev probe            | vendor | default variant                                      |
|-----------------------|--------|------------------------------------------------------|
| `nvidia*` nodes       | nvidia | newest `cuXXX` the detected driver supports (cu12x if driver unknown) |
| `/dev/kfd`            | rocm   | newest `rocmX.Y` (x86_64)                             |
| `/dev/dri/*` only     | drm    | `xpu` (render nodes: likely an intel gpu)            |
| nothing               | cpu    | `cpu`                                                 |

`cpu` is strictly the fallback: it is only the default when no accelerator
device nodes exist at all, or when the matched family is not in the index.

The pytorch recipe does not hardcode a variant. It reads
https://download.pytorch.org/whl/ (a plain pypi HTML index) at install time
and preselects the hardware-matched default from the live list. On an
interactive terminal it prints every variant — cuXXX gated by the detected
nvidia driver, rocm x.y, xpu, cpu marked as fallback — and asks; press enter
to take the default, or type a number/name for an expert override.
Non-interactive runs (docker, scripts) use the default silently, and
`--index` skips the whole thing. If the index is unreachable, an offline
best-guess from the table above is used.

Hardware-agnostic: the same binary runs on amd64/arm64, NVIDIA/AMD/Intel/CPU
hosts and picks the right wheels at install time.

## Subcommands

```
rocc init [cmd ...]  # run as PID 1: sshd if keys are found, supervise cmd (default: sleep infinity)
rocc gpu             # detected accelerators as JSON (great for debugging --gpus)
rocc keys            # keys discovered from the environment
rocc install ...     # install recipes now
rocc version
```

`rocc` with no arguments is `rocc init`: it supervises `sleep infinity` so
the container stays alive as an ssh appliance. Unknown commands are an
error, never a guess.

## PID 1 behavior

- **Zombie reaping**: a SIGCHLD-driven `wait4(-1, WNOHANG)` drain reaps
  orphaned processes (sshd session leftovers, user daemons). Every child is
  spawned through one locked spawn path, so no exit status is ever lost or
  stolen between fork and register.
- **Signals**: `SIGTERM`/`SIGINT` are forwarded to the main process (10s
  grace, then `SIGKILL`); `SIGHUP` restarts sshd.
- **Exit codes**: the container exits with the main process's exit code;
  if the main command can't start and sshd isn't running, rocc exits 127
  instead of idling as a dead container.
- **sshd supervision**: restarted with capped backoff (1s → 30s) if it dies.
- **PAM tolerance**: generates a PAM-free sshd config automatically on
  distros whose sshd is built without PAM (e.g. Alpine).
- **No installs at boot**: a flaky mirror can never delay or break boot;
  installs are an explicit `rocc install` away.

## Security notes

- Public-key auth only; passwords and challenge-response are disabled.
- Key discovery authorizes any public key found in the environment — don't
  pass keys you don't trust into a container where rocc runs sshd.
- `StrictModes no` keeps things robust on odd images; authorized_keys are
  still written 0600 with a 0700 `.ssh`.
- Host keys are generated on first boot; mount a volume at `/etc/ssh` to
  keep them stable across restarts.

## Easter egg

rocc is not a compiler, and it will tell you so:

```
$ rocc main.c -o main
rocc no compile. rocc only install and init.
```

It exits 1, like a compiler that failed you.

## Building

```
make build          # static binary at bin/rocc, no cgo
make test
GOARCH=arm64 make build   # cross-compile for other hosts
```

The module path is just `rocc` — no domain, on purpose. The Go toolchain
only treats a module path's first *dotted* element as a network location, so
a dotless first element means the module is permanently local: nothing to
fetch, nothing to squat. If you ever publish this, rename the module (and
imports) to a domain you actually control first.

## Layout

```
main.go               entry point: subcommand dispatch, install flag parsing
internal/boot/        PID-1 boot flow and signal handling
internal/proc/        zombie reaper, tracked children, supervisor, run helpers
internal/ssh/         key discovery, authorized_keys, openssh install, sshd
internal/install/     install recipes (uv, pytorch, passthroughs)
internal/gpu/         /dev probing and torch index selection
internal/bait/        compile-bait detection (rocc is not a compiler)
internal/util/        logging, env parsing, shell quoting
```
