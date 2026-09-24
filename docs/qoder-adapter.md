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

AO never downloads or packages the provider CLI. Qoder CLI ships two builds
with identical flag surfaces:

| Build | Binary | Config root | Login methods | Install |
|-------|--------|-------------|---------------|---------|
| China (CN) | `qoderclicn` | `~/.qoder-cn` | phone, Aliyun, GitHub | `curl -fsSL https://qoder.cn/install \| bash` |
| International | `qodercli` | `~/.qoder` | GitHub, Google | official installer / desktop CLI entry |

The adapter prefers the CN build when both are installed, and resolves the
binary in this order:

1. `qoderclicn` then `qodercli` on PATH
2. `/usr/local/bin/`, `/opt/homebrew/bin/` (both names)
3. `~/.local/bin/qoderclicn`, `~/.qoder-cn/bin/qoderclicn/qoderclicn`
4. `~/.local/bin/qodercli`, `~/.qoder/bin/qodercli/qodercli`

## 5. Authentication

- Login: `qoderclicn login` (CN build; phone/Aliyun/GitHub) or
  `qodercli login` (international; GitHub/Google). Interactive, opens a
  browser. This is a human authorization step; AO cannot perform it.
- Readiness probe: `<binary> status -o json` → `{"logged_in": bool}`. The
  adapter parses this bounded probe; an unparseable result is `unknown`, never
  a guess.
- Host-provided credential: when `QODER_SDK_AUTH_PAYLOAD_FILE` points at an
  existing non-empty file (desktop-app-hosted runs), the adapter treats that
  as authorized without spawning the probe.
- The standalone CLI does **not** share credentials with the Qoder desktop app
  (verified for both builds: `status` reports `logged_in: false` on a machine
  with a logged-in desktop app; the desktop keeps its tokens elsewhere, e.g.
  the macOS keychain). A separate one-time `login` is required.

## 6. ACP (Chat mode)

Verified against `qoderclicn/qodercli 1.1.62` by direct stdio handshake and
live AO sessions:

- `initialize` → `protocolVersion: 1`, `agentInfo {name: "qoder-cli-cn"}` (CN)
  / `"qoder-cli"` (international).
- Capabilities: `loadSession: true`; session capabilities `resume/fork/list/
  close/delete/additionalDirectories`; prompt capabilities `image`,
  `embeddedContext`; MCP `http`/`sse`; `_meta.qoder.promptQueueing`.
- `authMethods: [{id: "qoderclicn-login"}]` (CN) / `qodercli-login`.
- Unauthenticated `session/new` → JSON-RPC error `-32000 "Authentication
  required"`, exactly the code AO's generic ACP driver maps to
  `ErrChatAuthRequired`.
- `session/new` returns `modes` and `configOptions`; ACP mode ids are
  camelCase (`default`, `acceptEdits`, `auto`, `dontAsk`,
  `bypassPermissions`) and model config options use provider-internal ids
  (see §8).

`--acp` is an official but help-hidden flag of both builds.

### Permission handling in Chat

Permissions are passed as the CLI's own `--permission-mode`
(`accept_edits`/`auto`/`bypass_permissions`; AO default emits no flag) rather
than an ACP session mode. Verified end-to-end: a project configured
`bypass-permissions` ran a file-editing worker turn with no parked approvals.
The camelCase ACP mode ids are now confirmed, so switching to
`session/set_mode` is a possible follow-up; interactive approvals flow over
ACP `session/request_permission` either way and park in AO's approval UI.

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
- **Two vocabularies (verified):** the CLI `-m` accepts display names from
  `qoderclicn --list-models` (e.g. `DeepSeek-Flash`), while ACP
  `configOptions`/`models` advertise provider-internal ids (e.g. `dfmodel`).
  AO Chat sessions validate the model against the ACP-advertised ids, so
  `ao spawn --model` must carry the internal id for chat mode
  (`auto`, `qmodel_38max`, `qfmodel`, `qmodel_latest`, `qmodel`,
  `q37fmodel`, `dmodel`, `dfmodel`, `gmodel`, `gfmodel`, `gm51model`,
  `kmodel_latest`, `kmodel`, `mmodel` as of CN 1.1.62). The TUI `-m` display
  names are accepted by the CLI but would fail ACP validation. Wiring a
  model catalog discovery (mapping names↔ids) is a documented follow-up;
  `customModelEntryMode` stays `none` for now.

## 9. Qwen execution path (verified)

Qwen models are native to the account catalog: `qfmodel` (Qwen3.8-Flash) is
currently **0.00x Credit (free)**; `qmodel_38max` (Qwen3.8-Max) and others
are paid tiers. The Phase D/E end-to-end runs in this fork used
Qwen3.8-Flash via `--model` — AO passes the model through unchanged;
switching models never changes AO's architecture.

## 10. DeepSeek execution path (verified)

DeepSeek is natively available on the CN account — no BYOK needed despite
`allow_byok: 0`:

- `qoderclicn -p -m DeepSeek-Flash` → replied correctly; usage reported under
  `dfmodel`, billed **0.56 credits** per minimal turn from the same Qoder
  account credit pool.
- `ao spawn --harness qoder --mode chat --model dfmodel` → session ran,
  `settings.model: dfmodel`, turn completed with the expected reply.
- `dmodel` = DeepSeek-V4-Pro is also in the catalog.

The upper AO layer is model-agnostic: Qwen by day, DeepSeek by night is a
`--model` switch, nothing more.

## 11. Known limitations

- Login is interactive (browser); AO surfaces `needs auth` until the user runs
  `qoderclicn login` / `qodercli login`. Verified: after CN browser login,
  `status -o json` flips to `logged_in: true` and AO readiness turns green.
- The CLI keeps its own credentials — the desktop app login is not shared
  (verified for both builds); a one-time CLI login is required. Same account,
  same credit pool (usage billed in credits against the account).
- Model vocabulary split between CLI display names and ACP internal ids
  (see §8); no catalog mapping wired yet.
- Do not launch the AO daemon from inside a Qoder desktop agent session
  without scrubbing env: the inherited `QODER_AGENT_SDK_ENTRYPOINT` forces
  the CLI into private SDK stream-json mode. The adapter neutralizes it for
  agent launches (`AugmentRuntimeEnv`), but daemon-side probes like
  `--list-models` run in the daemon's own env.
- TUI worker sessions need `tmux` on the host (AO terminal runtime
  requirement, not Qoder-specific). Chat/ACP mode needs no tmux.
- Hooks: only the conservative set (SessionStart, UserPromptSubmit,
  PermissionRequest, Stop) is installed until Qoder's hook payloads are
  verified to carry Claude-Code-identical `tool_use_id` fields; the
  PreToolUse/PostToolUse trio (finer blocked-state correlation) is a
  documented follow-up. Hook timeout unit (seconds, Claude convention) is
  not yet verified on a live TUI worker.
- No AO-managed install plan (`systeminstall`) or frontend avatar yet; the
  agent list renders with a fallback avatar.

## 12. Debugging

- Readiness/auth: `ao agent list` (shows `installed` / `needs auth`).
- Direct auth probe: `qoderclicn status -o json` (CN) or `qodercli status -o json`.
- ACP handshake by hand:
  ```bash
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true}}}}' \
    | qoderclicn --acp
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

Live (env-gated; uses the local qodercli/qoderclicn install and account):

```bash
AO_PROBE_QODER_ACP=1 go test ./internal/adapters/chatdriver/qoderacp/ -run TestLiveQoderACPHandshake -v
AO_LIVE_QODER_ACP=1  go test ./internal/adapters/chatdriver/qoderacp/ -run TestLiveQoderACP -v
```

Both passed against CN 1.1.62 with a logged-in account (handshake 6.4s,
full turn 12.2s).

End-to-end worker run (after login; verified evidence in this fork):

```bash
ao daemon &
ao project add --path /tmp/hello-qoder-test --worker-agent qoder
ao project set-config hello-qoder-test --default-branch main \
   --permission bypass-permissions --model qfmodel
ao spawn --project hello-qoder-test --harness qoder --mode chat --name e2e \
         --prompt "<task that edits a file and runs a test>"
ao session get <session-id>          # status: working → idle
curl -s 127.0.0.1:<port>/api/v1/sessions/<id>/conversation  # turns/diff/messages
```

Review loop (verified): spawn a `claude-code` chat session as reviewer with a
SPEC and a required strict-JSON verdict line; on FAIL, `ao send --session
<worker>` the structured `required_fixes` back to the original Qoder worker;
re-review until PASS.

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
