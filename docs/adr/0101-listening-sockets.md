# ADR 101: Listening sockets

Status: accepted.

## Context

Since Go 1.24, listen sockets on Linux use Multipath TCP (MPTCP) by
default. CVE-2026-98432, a use-after-free in the kernel's IPv4 TCP
handling, was shown as remote code execution through an MPTCP listener.
Alborz's HTTP listener binds every interface by default, so a release
binary, the container image or the systemd unit exposes it directly.

## Decision

1. Binaries built from this module MUST disable MPTCP by default, through
   `godebug multipathtcp=0` in `go.mod`.
2. Listeners MUST NOT re-enable MPTCP per socket
   (`net.ListenConfig.SetMultipathTCP`).
3. Operators MAY override the default at runtime with
   `GODEBUG=multipathtcp=…`.
4. Installation documentation MUST tell operators to keep the host kernel
   patched; disabling MPTCP is defence in depth, not the fix.

## Consequences

- One module-wide line covers every listener; none can be forgotten.
- Clients gain nothing from MPTCP on a webmail server, so no feature is
  lost.
- The kernel bug is not specific to MPTCP; an unpatched host stays
  vulnerable to other paths.

## References

- CVE-2026-98432; golang/go#82717; https://go.dev/doc/godebug
- `go.mod`, `cmd/alborz/main.go`, `internal/desktop/runtime.go`,
  `INSTALLATION.md`
