# Qoder Adapter

Fork-local documentation for the Qoder CLI agent adapter (`qoder` harness).

## 1. Why Qoder

Qoder CLI (`qodercli`) is a Claude-Code-shaped coding agent CLI. This fork adds
it as a first-class AO worker harness so Qoder can serve as a low-cost Executor
in the Orchestrator → Worker → Review → Feedback loop, with models (Qwen,
DeepSeek, …) chosen underneath the harness rather than baked into AO.

## 2. Architecture

```
AO core (session_manager / daemon)
  → ports.Agent               ← adapters/agent/qoder        (TUI + spawn argv, hooks, auth)
  → ports.ChatDriver          ← adapters/chatdriver/qoderacp (Chat over ACP)
                                   ↓ nativeacp → chatdriver/acp (generic ACP transport)
                                   qodercli --acp  (Agent Client Protocol, protocolVersion 1)
```

- `backend/internal/adapters/agent/qoder/` — TUI/spawn adapter: binary
  discovery, launch/restore argv, workspace-local hooks, auth probe.
- `backend/internal/adapters/chatdriver/qoderacp/` — Chat driver binding
  `qodercli --acp` to AO's reusable ACP transport via `nativeacp`. It reuses the
  same agent plugin for binary resolution and auth, so Chat and TUI never
  disagree about "is Qoder installed and logged in".
- AO core changes: none beyond the standard one-line registration points every
  harness uses (domain constant, two registries, activity deriver map, DTO
  enums, CLI help text).

## 3. Dependencies

- Qoder CLI ≥ 1.1.62 (verified build; `qoderacp` version-gates on it).
- No Node runtime, no ACP bridge package: `qodercli --acp` speaks ACP natively
  over stdio.
- Go toolchain per `go.work` for building AO itself.

## 4. Installation

AO never downloads or packages the provider CLI. Install Qoder CLI yourself
(official curl-bash installer or the Qoder desktop app's CLI entry). The
adapter resolves the binary in this order:

1. `qodercli` on PATH
2. `/usr/local/bin/qodercli`, `/opt/homebrew/bin/qodercli`
3. `~/.local/bin/qodercli` (official installer symlink)
4. `~/.qoder/bin/qodercli/qodercli`

## 5. Authentication

- Login: `qodercli login` (interactive, opens a browser). This is a human
  authorization step; AO cannot perform it.
- Readiness probe: `qodercli status -o json` → `{"logged_in": bool}`. The
  adapter parses this bounded probe; an unparseable result is `unknown`, never
  a guess.
- Host-provided credential: when `QODER_SDK_AUTH_PAYLOAD_FILE` points at an
  existing non-empty file (desktop-app-hosted runs), the adapter treats that
  as authorized without spawning the probe.
- The standalone CLI does **not** share credentials with the Qoder desktop app
  config root (`~/.qoder-cn` on the CN desktop build). Verified: `status`
  reports `logged_in: false` on a machine with a logged-in desktop app, and
  `--config-dir ~/.qoder-cn` does not change that.

## 6. ACP (Chat mode)

Verified against `qodercli 1.1.62` by direct stdio handshake:

- `initialize` → `protocolVersion: 1`, `agentInfo {name: "qoder-cli"}`.
- Capabilities: `loadSession: true`; session capabilities `resume/fork/list/
  close/delete/additionalDirectories`; prompt capabilities `image`,
  `embeddedContext`; MCP `http`/`sse`; `_meta.qoder.promptQueueing`.
- `authMethods: [{id: "qodercli-login"}]`.
- Unauthenticated `session/new` → JSON-RPC error `-32000 "Authentication
  required"`, exactly the code AO's generic ACP driver maps to
  `ErrChatAuthRequired`.

`--acp` is an official but help-hidden flag of qodercli.

### Permission handling in Chat

Permissions are passed as the CLI's own `--permission-mode`
(`accept_edits`/`auto`/`bypass_permissions`; AO default emits no flag) rather
than an ACP session mode: Qoder's initialize response does not advertise a
mode list, and an unverified `session/set_mode` id would fail session creation
outright. Interactive approvals still flow over ACP
`session/request_permission` and park in AO's approval UI.

## 7. Headless fallback

Chat/ACP is the primary path. If ACP ever regresses, qodercli has a full
headless surface the adapter could target instead:

```
qodercli -p "<task>" -o stream-json --session-id <uuid> \
         --permission-mode <mode> [-m <model>] [--no-session-persistence]
```

- Output formats: `text`, `json`, `stream-json` (plus `--input-format`,
  `--include-partial-messages`, `--replay-user-messages`).
- Session persistence: `--session-id`, `-r/--resume <id>`, `-c/--continue`,
  `--fork-session`, `--list-sessions`.
- `--fake-responses <path>` / `--record-responses <path>` provide an official
  test stub for deterministic integration testing without model spend.

Not wired into AO yet; documented as the fallback of record.

## 8. Model configuration

- Per-project config key `model` → `-m <model>` on launch/restore and the ACP
  `model` session option in Chat.
- `qodercli --list-models` lists account-available models; `qodercli -m` also
  accepts custom-provider model IDs. AO treats Qoder strictly as a harness —
  Qwen/DeepSeek/etc. are provider-level concerns and are not AO core concepts.
- `customModelEntryMode` leaves qoder at `none` for now (no catalog discovery
  wired); model entry can be added later via `--list-models` parsing.

## 9. Qwen execution path

Qoder's default models are Qwen-family. Once logged in, `-m <model>` selects
among the account's models (verify with `--list-models`). AO passes the model
through unchanged; switching models never changes AO's architecture.

## 10. DeepSeek execution path

`qodercli status` reports an `allow_byok` field (observed `0` for this
account) and `-m` documents "Custom uses modelID", suggesting a BYOK/custom
provider surface. Whether DeepSeek can be configured depends on account
entitlement; not verified yet. If unavailable, the cleanest alternative is a
different AO harness wired to the DeepSeek API directly — no hack required.

## 11. Known limitations

- Login is interactive (browser); AO surfaces `needs auth` until the user runs
  `qodercli login`.
- CLI does not share the desktop app's session/credentials (see §5).
- TUI worker sessions need `tmux` on the host (AO terminal runtime
  requirement, not Qoder-specific).
- Hooks: only the conservative set (SessionStart, UserPromptSubmit,
  PermissionRequest, Stop) is installed until Qoder's hook payloads are
  verified to carry Claude-Code-identical `tool_use_id` fields; the
  PreToolUse/PostToolUse trio (finer blocked-state correlation) is a
  documented follow-up.
- Hook timeout is configured in seconds (Claude Code convention, matching
  qodercli's Claude-compatible hook system); not yet verified end-to-end on a
  live TUI worker.
- No AO-managed install plan (`systeminstall`) or frontend avatar yet; the
  agent list renders with a fallback avatar.
- `allow_byok`, membership/credit sharing between CLI and desktop account:
  unverified.

## 12. Debugging

- Readiness/auth: `ao agent list` (shows `installed` / `needs auth`).
- Direct auth probe: `qodercli status -o json`.
- ACP handshake by hand:
  ```bash
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true}}}}' \
    | qodercli --acp
  ```
- Chat driver probe: `AO_PROBE_QODER_ACP=1 go test ./internal/adapters/chatdriver/qoderacp/ -run TestLiveQoderACPHandshake -v`
- Daemon logs: `~/.ao/logs/` (or `$AO_DATA_DIR/logs`).

## 13. Test procedure

Unit (no network, no CLI required):

```bash
cd backend
go test ./internal/adapters/agent/qoder/... \
        ./internal/adapters/chatdriver/qoderacp/... \
        ./internal/adapters/agent/registry/... \
        ./internal/adapters/chatdriver/registry/... \
        ./internal/daemon/...
```

Live (env-gated; uses the local qodercli install and account):

```bash
AO_PROBE_QODER_ACP=1 go test ./internal/adapters/chatdriver/qoderacp/ -run TestLiveQoderACPHandshake -v
AO_LIVE_QODER_ACP=1  go test ./internal/adapters/chatdriver/qoderacp/ -run TestLiveQoderACP -v
```

End-to-end worker run (after `qodercli login`):

```bash
ao daemon &                                 # or the desktop app
ao spawn --standalone --harness qoder --mode chat --name qoder-e2e \
         --prompt "<task that edits a file and runs a test>"
```

## 14. Upstream compatibility

Registration follows the upstream-documented single-edit points
(`adapters/agent/registry.Constructors`, `chatdriver/registry.Build`,
`domain.AllHarnesses`, `activitydispatch.Derivers`, DTO enums + regenerated
contracts). Expected rebase friction after upstream updates:

- `dto.go` harness enum lines and regenerated `openapi.yaml`/`schema.ts`
  (upstream adds harnesses frequently — trivial textual conflicts).
- `wiring_test.go` / `chatdriver/registry_test.go` harness lists (same reason).
- `nativeacp`/`acp` driver Config signature changes would touch `qoderacp`
  exactly as they touch every native ACP binding.

The adapter packages themselves (`agent/qoder`, `chatdriver/qoderacp`) are
self-contained and should not conflict.
