# Architecture

`cmd/portman` composes small packages with platform-independent interfaces:

| Package | Responsibility |
| --- | --- |
| `internal/model` | Process identities, sockets, projects, snapshots, interfaces |
| `internal/platform` | Native inspection and OS-specific signaling |
| `internal/discovery` | macOS lsof parser; Linux procfs socket ownership |
| `internal/project` | Manifest walks, repository roots, ancestry, per-scan cache |
| `internal/classify` | Deterministic category heuristics |
| `internal/process` | Protection policy, revalidation, stop requests, exit polling |
| `internal/docker` | Local Engine discovery, Compose labels, published ports, container signals |
| `internal/engine` | Discovery, resolution, classification, protection |
| `internal/tui` | Bubble Tea state, grouping, rendering, shared mouse geometry |

## Platform boundaries

On macOS, cgo calls `PROC_PIDTBSDINFO`, `proc_pidpath` and
`PROC_PIDVNODEPATHINFO`. The inspector checks process start time before and after
metadata reads. `/usr/sbin/lsof` supplies NUL-delimited TCP/UDP socket records.
Processes born after the socket scan began are discarded to avoid associating
old records with a reused PID.

On Linux, `/proc/PID/stat` provides the parent, name, state and start ticks;
`status` provides effective UID, and `exe` / `cwd` provide paths. Mixed UIDs are
protected. Names containing spaces or parentheses are parsed without shifting
stat fields. Start tokens are opaque and only meaningful on the current OS/boot;
snapshots and selections are not persisted.

The Linux discoverer captures identities, reads `/proc/net/{tcp,tcp6,udp,udp6}`,
matches socket inodes against process file descriptors, deduplicates descriptors,
and revalidates identity, executable and UID before publishing each result.
Socket tables cover the caller's network namespace. Missing IPv6 tables are allowed;
missing required IPv4 tables fail the scan. Permission errors produce visibility
warnings. The backend does not enter other namespaces or require lsof/cgo.

## Termination

The manager checks both captured and current protection policies. Every target
is revalidated against PID, start token, executable and UID. Changed identities,
vanished processes and zombies count as exited; changed ownership/executable is
refused. Root, other users, critical system processes, incomplete identities and
Portman's terminal ancestry are guarded.

Linux opens a pidfd before another revalidation, then signals that handle. PID
reuse after that point cannot redirect the signal to a replacement process.
Failure to obtain or use a pidfd is reported; there is no numeric-PID fallback.
This requires Linux 5.3+ and syscalls permitted by the environment. macOS uses
`kill(2)` after revalidation; an unavoidable inspection-to-signal race remains.
Neither platform freezes a process during executable/credential inspection.

Docker workloads carry a separate identity: Unix endpoint, full container ID,
and `StartedAt`. The engine routes container targets to the Docker client and host
targets to the process manager; the latter refuses container objects defensively.
Docker host processes (including `com.docker.backend`, `dockerd`, `docker-proxy`
and `containerd`) are always protected.

The Docker CLI is used only to resolve the active context. The Engine client uses
Go's HTTP transport over the resolved local Unix socket, without proxying or
following redirects. Remote TCP/SSH contexts are rejected. Discovery is optional
and bounded to three seconds; failed/partial discovery leaves host results intact.
Only mapped Docker-helper sockets are replaced, preserving unmatched host sockets.
Container inspection decodes names, state, image, Compose labels and published
TCP/UDP bindings; environments are not exposed. Running internal-only services
are included. Paused, restarting and host-network containers are protected.

Explicit `POST /containers/{full-id}/kill?signal=SIGTERM` requests never automatically
escalate. Survivors may receive SIGKILL only after a separate confirmation. Both
requests revalidate `StartedAt` and protection. Polling rejects restarts, permission
failures and ambiguous state instead of offering a force action against them.
Docker has no atomic start-time precondition for signaling, so a restart between
inspection and signal remains a documented race. No stop request removes volumes
or invokes Compose-wide commands.

Signals target individual positive PIDs only. A stop request never grows after
confirmation. SIGKILL requires a separate user confirmation after SIGTERM; no
permission or inspection error triggers escalation. Polling treats exit, zombie
state and PID reuse as departure of the original process.

## Refresh and rendering

One asynchronous scan runs at a time. Previous data stays visible until a scan
succeeds. Socket-mode changes schedule a new scan and ignore outdated results.
Selection uses `PID:start-token` for hosts and endpoint/ID/start-time for containers;
group keys use canonical project roots or endpoint/Compose name.
Disappeared selections are pruned. External strings are sanitized before terminal
rendering. The renderer inherits the terminal background and uses ANSI-aware
cell widths with shared render/hit-test geometry.

The Docker tree has Compose groups (or a standalone group), containers, and optional
port rows. Port rows resolve to their owning container for actions. Root and nested
group counts are deduplicated. Container port expansion defaults to collapsed and
is retained across refreshes. A filtered group includes only matching workloads;
expanding a container shows all its bindings to make the stop scope visible.

`preview_test.go` exports synthetic fixtures through the production renderer;
there is no demo mode in the executable.

## Installation and packaging

`VERSION` is the source of release versions. `scripts/package.sh` builds the
selected Go target into `dist/portman_OS_ARCH.tar.gz` and writes a companion
SHA-256 file. Linux builds use `CGO_ENABLED=0`; macOS builds use cgo and the Apple
SDK. Native CI runners build and test all four platform/architecture combinations.

`install.sh` has three modes:

- `--archive FILE`: verify the adjacent SHA-256, extract only the executable,
  check it runs, and atomically replace the destination via a same-directory rename.
- `--repo OWNER/REPO [--version TAG]`: download the detected OS/CPU archive and
  checksum from GitHub Releases using HTTPS, then follow the same install path.
- `--source`: compile this checkout using Go. This is also the default when the
  script is run from a source checkout without an explicit mode.

Installation defaults to `~/.local/bin`; `--prefix` changes the parent directory.
No sudo, shell-profile changes, Go bootstrap, or Homebrew are required for binary
installation. Checksums detect corruption; they are not independent signatures.
Users must trust the repository that supplies the archive and checksum.

The existing macOS Homebrew installer creates a local source tap with a real
checksummed `file://` archive. It automatically brings in Go as a build dependency.
It is intentionally separate from portable binary installation.

## CI and publishing

The workflow uses `macos-14` (ARM64), `macos-15-intel`, `ubuntu-24.04` (x86-64),
and `ubuntu-24.04-arm`. Each job runs formatting, vet, race tests, real-process
integration tests, packaging, installation tests and a PTY smoke test of the
installed binary. Installation tests assert TCP discovery, source installation,
reinstallation, spaces in paths, corrupt/missing checksums and no Go invocation
for binary packages. Temporary prefixes protect the developer's actual installation.

Docker API fixtures exercise discovery, permissions, restarts, protection and
signal scope on every OS. `scripts/smoke-docker.py` additionally creates isolated
containers with Compose labels and drives the installed TUI through a PTY. It
checks TCP/UDP mappings, keyboard/mouse selection, cancel-by-default dialogs,
TERM then separately confirmed KILL, and that sibling/other-project containers
stay running. Cleanup uses only IDs created by that invocation. This runs in
the Ubuntu x86-64 CI job and can be run locally with Docker available.

For a public release:

1. Publish this checkout to the intended GitHub repository with Actions enabled.
2. Set `VERSION` and push a matching `vX.Y.Z` tag.
3. All four test jobs must succeed. The tag-only release job checks `VERSION`,
   downloads their artifacts, and uploads the archives, checksums and installer.
4. Replace the README's clearly marked `OWNER/REPO` example with the real repository.
5. If a public Homebrew tap is wanted, publish a formula referencing a real hosted
   source archive and checksum. The local tap is not advertised as public.

No repository or public release URL is assumed by the source checkout. Ordinary
pushes and pull requests only test and upload Actions artifacts; they do not publish
releases. Release publishing uses a job-scoped write token; tests need only read access.

## References

- [Bubble Tea](https://github.com/charmbracelet/bubbletea)
- [Lip Gloss](https://github.com/charmbracelet/lipgloss)
- [Apple proc_info.h](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/proc_info.h)
- [Linux proc filesystem](https://docs.kernel.org/filesystems/proc.html)
- [Linux TCP table format](https://docs.kernel.org/networking/proc_net_tcp.html)
- [pidfd_open](https://man7.org/linux/man-pages/man2/pidfd_open.2.html)
- [pidfd_send_signal](https://man7.org/linux/man-pages/man2/pidfd_send_signal.2.html)
- [Docker Engine API](https://docs.docker.com/reference/api/engine/)
- [Docker stop and automatic escalation](https://docs.docker.com/reference/cli/docker/container/stop/)
- [GitHub hosted runners](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
