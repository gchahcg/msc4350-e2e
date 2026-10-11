# MSC4350 session status

Last updated: 2026-10-10 (session closed while waiting on maintainers)

## Where we are

Waiting on maintainer responses. Nothing is blocked on my side.

| Item | State |
|---|---|
| Room message in mautrix Go Matrix room | posted (text was drafted via write-as-me MCP, written by me) |
| mautrix/go PR 605 | open: https://github.com/mautrix/go/pull/605 (combined sender+receive, 8 commits, `bridgev2: implement MSC4350 impersonatable device support`) |
| matrix-org/matrix-rust-sdk PR 7251 | open: https://github.com/matrix-org/matrix-rust-sdk/pull/7251 (`feat(crypto): add MSC4350 support with feature flag, persistence, and membership checks`, AI checkbox checked per repo policy) |
| Changelog fragment | renamed to `7251.added.md`, pushed |
| Issues + MSC spec comment | not posted yet (drafts in `drafts/`) |

## What's left (resume order)

1. Watch the two PRs + the Matrix room. If maintainers ask for changes, iterate (text via MCP, code by me).
2. Post the rust-sdk issue from `drafts/rust_sdk_issue.md` (via MCP relay, my words).
3. Post the MSC4350 spec comment from `drafts/msc_implementation_report.md` (check matrix-spec-proposals AI policy first — none found in CONTRIBUTING.md/README).
4. `drafts/issue_body.md` (mautrix issue) is now stale — the PR is already open; only use it if a maintainer asks for the write-up.
5. Optional cleanup: fork branch `msc4350-ghost-impersonation` (sender-only) is superseded by the combined PR; delete from fork if we want tidy.

## Repo / branch map

- `/home/k2s0/dev/go` — mautrix/go. origin = gchahcg/go (fork), upstream = mautrix/go.
  - `msc4350-receive-validation`: the combined MSC4350 branch, rewritten history (no Co-Authored-By trailers), tip `cbec53a2`, pushed to fork, = PR 605 head.
  - `msc4350-ghost-impersonation`: sender-only, superseded.
- `/home/k2s0/dev/rust-msc4350/matrix-rust-sdk` — origin = matrix-org (upstream), fork = gchahcg.
  - `msc4350-hardening`: rebased onto upstream main (`27546464c`), all commits DCO-signed, tip `0bb05b3a7b` (changelog rename), pushed to fork, = PR 7251 head.
- `/home/k2s0/dev/msc4350-e2e` — the E2E harness (throwaway Synapse + fake bridgev2), public at github.com/gchahcg/msc4350-e2e.

## Ground rules we kept

- All posted text (room message, commit messages, PR titles/bodies) went through the write-as-me MCP relay: MCP hands facts to me, I write, MCP returns. Final texts recorded back via record_final (voice model is improving).
- No AI/assistant attribution anywhere: no Co-Authored-By trailers, no "generated with AI" lines. Only the rust-sdk mandatory disclosure checkbox.
- Mautrix policy: announce in Matrix room before PR (done); PR text written by the author.
- Rust-sdk policy: disclose AI help (checkbox), DCO sign-off, changelog fragment named after PR number.

## Evidence on file

- mautrix: `go test ./crypto/ ./bridgev2/matrix/ ./` all ok (go 1.27.1) on the final branch; E2E against throwaway Synapse.
- rust-sdk: `cargo test -p matrix-sdk-crypto --features experimental-msc4350` → 488 passed / 0 failed / 2 ignored + 32 doctests (post-rebase); clippy clean both ways; E2E via mautrix.
- Cross-impl: ghost device produced by the Rust code is accepted by the Go implementation and vice versa (test vector `crypto/testdata/msc4350_rust_ghost_device.json`).

## Notes for the next session

- `/tmp/opencode/` artifacts (pr diffs, commit-message files, PR body files) may be gone after reboot — everything they held is now on GitHub (PRs, commit history) or in `drafts/`.
- Cargo needs `export PATH="$HOME/.cargo/bin:$PATH"` in new shells.
- `MY-TODO.md` in `drafts/` is my personal verification checklist (read every commit, break things on purpose, etc.) — separate from this status file.
