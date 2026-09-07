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

No configuration environment variables: keys are discovered by value,
hardware by probing /dev, everything else you ask for explicitly.

Boot sequence:

```
rocc init (PID 1)
 ├─ probe /dev for accelerators (logged; `rocc gpu` to list them)
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

(arm64: `rocc_linux_arm64`; releases also carry `.sha256` checksums)

- sshd starts because a public key was discovered in `SSH_KEY`
- openssh-server is installed automatically if missing
- `ssh -p 2222 root@localhost` — public key only, no passwords

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

## Running custom containers

rocc works with any image and any start command. Nothing is injected: no
build step, no required base — the image stays stock and rocc is fetched
(or copied in; see the Dockerfile under [Releasing](#releasing)) at start.
The flip side: rocc has to be the container's first process; it cannot be
attached to a container that is already running.

### Flaky start commands (RunPod & friends)

Some platforms mangle the container start command — quotes and spaces get
eaten, or the field resets between restarts. Env vars survive what command
fields don't, so carry the whole bootstrap in one and make the start
command a fixed incantation:

Env vars — one key, one value, each a single line, ready to paste into
key/value fields:

| key | value |
|-----|-------|
| `KEYS` | your public key, e.g. `ssh-ed25519 AAAA... you@laptop` |
| `ROCC` | `apt-get update -qq && apt-get install -y -qq curl && curl -fsSL https://github.com/Ristellise/rocc-init/releases/latest/download/rocc_linux_amd64 -o /usr/local/bin/rocc && chmod +x /usr/local/bin/rocc && exec rocc init` |

Docker command:

```
bash -c eval${IFS}$ROCC
```

- `KEYS`: any name works; a path to a key file works too
- `ROCC`: drop the `apt-get` prefix if the image ships curl; append a
  workload with `... && exec rocc init -- <cmd args...>`
- `${IFS}` expands to a space at eval time — no quotes, no literal space,
  nothing for a flaky parser to eat. If a shell evaluates the command
  first, quote or escape it: `bash -c 'eval${IFS}$ROCC'` or
  `bash -c eval\${IFS}\$ROCC`
- the `exec` makes rocc PID 1; `ROCC` is read by bash, never by rocc

### When the command field behaves

Everything after `init --` is the main process, supervised under PID 1.

- `SIGTERM`/`SIGINT` are forwarded (10s grace, then `SIGKILL`); the
  container exits with the workload's exit code
- installs compose into the start command:
  `rocc init -- bash -c 'rocc install pytorch && <cmd>'`
- sshd still runs alongside when keys are discovered; with no start
  command, `rocc init` supervises `sleep infinity` (the ssh appliance)

## Releasing

Tag a commit and push the tag; the [release workflow](.github/workflows/release.yml)
runs vet + tests, builds static binaries for linux/amd64 and linux/arm64,
stamps `rocc version` with the tag, and attaches them (plus `.sha256`
checksums) to the GitHub release:

```sh
git tag v0.6.0
git push origin v0.6.0
```

If you prefer baking a minimal image instead of pulling at start:

```dockerfile
FROM ubuntu:24.04
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends curl && rm -rf /var/lib/apt/lists/*
COPY rocc /usr/local/bin/rocc
ENTRYPOINT ["/usr/local/bin/rocc", "init"]
# CMD ["jupyter", "lab"]   # optional: runs supervised under PID 1
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

Variables that hold *host* keys (`KNOWN_HOSTS`, `*_HOST_KEY`) are skipped
both ways.

rocc never fetches anything itself; fetch remote keys in the launcher:

```sh
-e KEYS="$(curl -fsSL https://github.com/<user>.keys)"
```

sshd comes up **only if at least one key is discovered**, so `docker run
image <cmd>` with no keys in the environment stays a normal container.

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

The pytorch recipe reads the index live at install time instead of
hardcoding a variant, and verifies the candidate ships wheels for this
platform and python before preselecting it (the index has partial dirs —
rocm7.14 has no cp312 x86_64 torchaudio), walking older until one does.
Interactively it offers the newest few builds plus the cpu fallback and
asks; `--index` overrides. If the index is unreachable, the table above
is the offline guess.

## Subcommands

```
rocc init [cmd ...]  # run as PID 1: sshd if keys are found, supervise cmd (default: sleep infinity)
rocc gpu [--json]   # detected accelerators (key: value list; --json for machines)
rocc keys            # keys discovered from the environment
rocc install ...     # install recipes now
rocc version
```

Bare `rocc` means `rocc init` as pid 1; from a shell it prints help.
Unknown commands are an error, never a guess.

## PID 1 behavior

- **Zombie reaping**: a SIGCHLD-driven `wait4(-1, WNOHANG)` drain reaps
  orphans; all children go through one locked spawn path, so no exit
  status is lost or stolen.
- **Signals**: `SIGTERM`/`SIGINT` are forwarded to the main process (10s
  grace, then `SIGKILL`); `SIGHUP` restarts sshd.
- **Exit codes**: the container exits with the main process's exit code;
  if nothing is left to supervise, rocc exits 127 instead of idling.
- **sshd supervision**: restarted with capped backoff (1s → 30s); if port
  22 is held by another process it waits instead of thrashing.
- **PAM tolerance**: PAM-free sshd config on distros whose sshd is built
  without PAM (e.g. Alpine).
- **No installs at boot**: a flaky mirror can never delay or break boot.

## Security notes

- Public-key auth only; passwords and challenge-response are disabled.
- Key discovery authorizes any public key found in the environment — don't
  pass keys you don't trust into a container where rocc runs sshd.
- `StrictModes no` keeps things robust on odd images; authorized_keys are
  still written 0600 with a 0700 `.ssh`.
- Host keys are generated on first boot; mount a volume at `/etc/ssh` to
  keep them stable across restarts.

## Building

```
make build          # static binary at bin/rocc, no cgo
make test
GOARCH=arm64 make build   # cross-compile for other hosts
```

The module path is `rocc` — dotless, so permanently unresolvable: nothing
to fetch, nothing to squat. Rename it (and the imports) if you ever
publish.

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
