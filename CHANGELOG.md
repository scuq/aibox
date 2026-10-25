# Changelog

All notable changes to aibox. The format follows Keep a Changelog; versions
are semver git tags.

## 0.1.0 - 2026-10-26

First tagged release. aibox is a single static Go binary that runs Claude
Code and Codex in disposable, hardened, rootless podman containers with an
allowlisting egress proxy, a TCP relay for named services, encrypted
access-gated secrets, a host-shared scratch directory, and generated
environment notes for the agent.

### Changed

- Git policy: `git.history: read-write` is the new default. Git writes work
  inside the container, and the environment notes plus the built-in aibox
  skill make them an ask-first policy: the agent shows the command and the
  message and waits for the user's yes before every commit, push and tag,
  and keeps AI attribution out of commit messages, PR/MR text, issues and
  comments. `read-only` (kernel-enforced, loud shim, handoff) and `none`
  remain available as opt-in modes.
- The host `user.name`/`user.email` are forwarded into the container as
  `GIT_AUTHOR_*`/`GIT_COMMITTER_*` (`git.identity`, default on). The host
  `~/.gitconfig` is still never mounted.

### Added

- `gh` and `glab` in the image, pinned and checksummed per architecture.
  Forge writes are allowed under the same ask-first rule; tokens come from
  `/creds` per command. `api.github.com`, `uploads.github.com` and
  `gitlab.com` join the base egress allowlist.
