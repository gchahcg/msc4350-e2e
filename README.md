# MSC4350 end-to-end tests

Automated version of runbook Chunk 7 for the `msc4350-ghost-impersonation` branch of mautrix-go.
It brings up a throwaway Synapse in Docker, runs a tiny fake bridgev2 bridge built from the local
branch of the fork (see the `replace` in `go.mod`, which pins the commit under test), and asserts on `/keys/query`,
the bridge logs and the bridge database. Nothing in `/opt/stacks/element` is touched.

```
./run.sh all            # up, run all scenarios, tear down (exit code = test result)
./run.sh up             # just bring the stack up (then ./run.sh test [-run T2], ./run.sh down)
./run.sh keep           # stack + Element over self-signed HTTPS, for the one-off manual look
./run.sh say [text]     # (keep mode) send an encrypted message from a bridge ghost to alice
./run.sh down           # tear down; logs of the last run are kept in artifacts/last/
```

## Scenarios (tests/e2e_test.go)

| Test | Runbook | What it checks |
|---|---|---|
| T1 | step 1 | first message from a new ghost logs exactly one registration; a second message logs none |
| T2 | step 2 | `/keys/query`: ghost device ID = bot device ID, `algorithms: []`, `keys: {}`, only the bot signed it, impersonator == bot's device keys minus signatures, bot signature verifies and fails on tampering |
| T3 | step 3 (API part) | a real client (cryptohelper) decrypts the ghost's message, and the encrypted event's `device_id` is the ghost's registered device |
| T4 | step 4 | bridge restart: no second registration, same single ghost device, one `crypto_impersonatable_device` row |
| T5 | step 5 | `msc4350` without `msc4190`: startup warning, no registration, no ghost devices, messages still delivered |
| T6 | extra | 8 concurrent first messages from one ghost register it once (singleflight) |
| T7 | extra | negative control: `msc4350: false` means no ghost devices |
| T8 | extra | the bot's own device keeps its keys and is not impersonatable |

Sanity check of the harness itself: `./run.sh up nomsc4350 && ./run.sh test` makes T1, T2, T3 and T6 fail.

## Layout

- `compose.yml`: Synapse (same image digest as the live stack), plus optional Element and Caddy (`--profile element`).
- `patch_synapse_config.py`: patches a generated `homeserver.yaml` (same experimental flags as the live stack, no MAS, no rate limits). `legacy` mode turns `msc4190_enabled` off.
- `render_bridge_config.py`: renders the fakebridge config. Variants: `default`, `nomsc4350`, `nomsc4190`.
- `fakebridge/`: `mxmain.BridgeMain` plus a minimal connector. `POST /inject` makes a remote "ghost" send a message to a Matrix user, which creates the portal and an encrypted room.
- `tests/`: scenarios and helpers. `internal/e2e`: user registration and login helpers.

## Differences from the live stack

- No MAS (MSC3861); plain Synapse with SQLite. Appservice masquerading and MSC4190 behave the same, but this has not been run against the MAS-delegated live homeserver.
- T5 needs a homeserver in "legacy" mode (`msc4190_enabled: false` and an appservice registration without `io.element.msc4190`), because with MSC4190 on, a bridge configured without it cannot even log in.

## Receive-side check in a real client (Element Web with a patched crypto)

Element's crypto (matrix-rust-sdk, compiled to wasm) doesn't implement the receiving side of MSC4350 yet, so Element
keeps showing "The sender of the event does not match the owner of the device who sent it" for bridged messages.
`./run.sh keep` brings up Element behind a self-signed HTTPS proxy, and `./run.sh wasm <file|stock>` swaps its crypto
module for a build you provide, which is how a patched matrix-rust-sdk was tried against these tests. The patch
itself is a prototype and isn't published here.

```
./run.sh keep                  # stack + Element over HTTPS (login alice / alicepw, server test.local)
./run.sh say "text" ghostname  # a new ghost per message makes a fresh room and key query
./run.sh wasm <file>           # serve Element with your own crypto wasm
./run.sh wasm stock            # back to the stock module
```

`tools/match_wasm_exports.py` renames the few compiler-hash dependent exports of a wasm built with a different rustc
than Element's, so it loads with Element's unchanged JS glue.

Notes from trying a receive-side implementation: Element decides a message's shield when it first decrypts it, which
for a freshly joined room is before the `/keys/query` for its members returns, so a re-check on device-list changes
is needed. A client also needs room-membership data to enforce the MSC's "impersonator is in the room" rule.

## Requirements

Docker (with compose), Go 1.26+, Python 3 with PyYAML, libolm headers (or build with the goolm tag), and curl.
The test homeserver and credentials are throwaway and only listen on localhost.

## License

Mozilla Public License 2.0, the same as mautrix-go. See `LICENSE`.
