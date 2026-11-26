# System requirements

## The Mac (runs macoun-relay)

- **macOS 13 to 15.** The relay uses `AAAbsintheContext` (NAC) and `AOSKit`
  (anisette), both private frameworks. Stay below macOS 27: AOSKit anisette is
  reported broken on 27 betas, and Apple has been restricting
  `AAAbsintheContext` for apps outside the OS. Treat macOS 26 as untested.
- **Intel works** (verified on a 2018 Mac mini, macOS 15.7.4). Apple Silicon
  is expected to work but is not verified.
- **Signed in to iCloud** with the Apple ID the bridge will use.
- **Always on.** No system sleep (`pmset sleep 0`), restart after power loss.
- **A logged-in GUI session.** The relay is a per-user LaunchAgent, and AOSKit
  needs an Aqua session. To come back after an unplanned reboot it needs
  auto-login, which in turn needs FileVault off. The trade-off: anyone with
  physical access gets a logged-in session and the disk is unencrypted at rest.
  With FileVault on, every unplanned reboot waits for someone to type the
  password.
- **Controlled updates.** Turn off automatic macOS installs. A new build can
  change the private frameworks; check `/health` and make one real
  `/validation-data` call after every update.
- **Go 1.25** and the Xcode Command Line Tools, to build the relay.

## The Linux host (runs macoun-matrix)

- amd64. The build is tested on Debian bookworm (glibc 2.36) in Docker.
- Docker, to build with `docker/Dockerfile.build`. A build needs about 6 GB of
  memory and 10 GB of disk for the Rust target directory and caches.
- A Matrix homeserver you control (tested design: Synapse), with an appservice
  registration for the bridge.
- Runtime libraries: `libolm`, `libunwind`, `zlib`; `libheif` is loaded at run
  time if present (HEIC conversion).

## Network

- The bridge must reach the relay's HTTPS port; nothing else needs to reach the
  Mac. A private network such as Tailscale is recommended; bind the relay to
  that interface only, never to the LAN or the internet.
- With Tailscale, disable key expiry for the Mac (and the bridge host), or the
  link drops when the node key expires.
- Latency is not critical: the relay is called a handful of times a day.
