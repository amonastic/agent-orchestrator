// Package qoder implements the Qoder CLI agent adapter: launching new
// sessions, resuming hook-tracked sessions, installing workspace-local native
// hooks, and reading hook-derived session info.
//
// Qoder CLI ships two builds with identical flag surfaces: the China build
// `qoderclicn` (config root ~/.qoder-cn, phone/Aliyun/GitHub login, installed
// via `curl -fsSL https://qoder.cn/install | bash`) and the international
// build `qodercli` (config root ~/.qoder, GitHub/Google login). The adapter
// prefers the CN build when both are installed. Both are Claude-Code-shaped
// coding agent CLIs: `-p/--print` for the headless one-shot prompt,
// `-i/--prompt-interactive <text>` to run a prompt and stay interactive,
// `--permission-mode {default,accept_edits,bypass_permissions,dont_ask,auto}`
// for permissions, `--session-id <uuid>` to pin the native session identity,
// `-r/--resume <id>` to continue a specific session, and `--acp` to serve the
// Agent Client Protocol over stdio (used by the qoderacp Chat driver). They
// also have a Claude-Code-shaped hook system configured in
// `.qoder/settings.json` or `.qoder/settings.local.json` (top-level "hooks"
// key, event arrays of matcher groups with command hooks), and emit a
// `session_id` in hook payloads — so AO captures native session identity and
// activity from those hooks rather than from transcript scans.
package qoder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/agentbase"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// adapterID is the registry id and the value users pass to `ao spawn --agent`.
const adapterID = "qoder"

// Plugin is the Qoder CLI agent adapter. It is safe for concurrent use; the
// binary path is resolved once and cached under binaryMu.
type Plugin struct {
	agentbase.Base
	binaryMu       sync.Mutex
	resolvedBinary string
}

// New returns a ready-to-register Qoder adapter.
func New() *Plugin {
	return &Plugin{}
}

var _ adapters.Adapter = (*Plugin)(nil)
var _ ports.Agent = (*Plugin)(nil)
var _ ports.AgentAuthChecker = (*Plugin)(nil)
var _ ports.AgentBinaryResolver = (*Plugin)(nil)
var _ ports.AgentBinaryResolutionInvalidator = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoff = (*Plugin)(nil)
var _ ports.AgentInterfaceHandoffHistoryProbe = (*Plugin)(nil)

// permissionConfigEnum lists the permission modes the "permissions" config key
// accepts. It mirrors the ports.PermissionMode constants so a project's stored
// config validates against the same vocabulary the launch command maps.
var permissionConfigEnum = []string{
	string(ports.PermissionModeDefault),
	string(ports.PermissionModeAcceptEdits),
	string(ports.PermissionModeAuto),
	string(ports.PermissionModeBypassPermissions),
}

// GetConfigSpec reports the per-project agent config keys Qoder CLI
// understands: a model override and a starting permission mode.
func (p *Plugin) GetConfigSpec(ctx context.Context) (ports.ConfigSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ConfigSpec{}, err
	}
	return ports.ConfigSpec{
		Fields: []ports.ConfigField{
			{
				Key:         "model",
				Type:        ports.ConfigFieldString,
				Description: "Model override passed to `qodercli -m` (see `qodercli --list-models`).",
			},
			{
				Key:         "permissions",
				Type:        ports.ConfigFieldEnum,
				Description: "Starting permission mode.",
				Enum:        permissionConfigEnum,
			},
		},
	}, nil
}

// Manifest returns the adapter's static self-description.
func (p *Plugin) Manifest() adapters.Manifest {
	return adapters.Manifest{
		ID:          adapterID,
		Name:        "Qoder",
		Description: "Run Qoder CLI worker sessions.",
		Version:     "0.0.1",
		Capabilities: []adapters.Capability{
			adapters.CapabilityAgent,
		},
	}
}

// GetLaunchCommand builds the argv to start an interactive Qoder CLI session.
// Shape:
//
//	qodercli [--session-id <uuid>] \
//	         [--permission-mode <mode>] \
//	         [-m <model>] \
//	         [--append-system-prompt <text>] \
//	         [-i <prompt>]
//
// --session-id pins Qoder's native session UUID to LaunchConfig.NativeSessionID
// when AO requests a distinct provider conversation, otherwise to a value
// derived from the AO session id, which makes the session resumable later (see
// GetRestoreCommand) without waiting for a hook report.
//
// AO's "default" permission mode emits no --permission-mode flag, so Qoder
// resolves the starting mode from its own settings exactly as a normal launch.
//
// The prompt is passed via -i/--prompt-interactive so Qoder executes it and
// stays interactive — the worker behavior AO wants for attachable sessions.
func (p *Plugin) GetLaunchCommand(ctx context.Context, cfg ports.LaunchConfig) (cmd []string, err error) {
	// Defense-in-depth: the project service validates on write, but re-check
	// here so a config written by any other path can't launch a bad command.
	if err := cfg.Config.Validate(); err != nil {
		return nil, fmt.Errorf("qoder: %w", err)
	}

	binary, err := p.qoderBinary(ctx)
	if err != nil {
		return nil, err
	}

	cmd = []string{binary}
	if id := strings.TrimSpace(cfg.NativeSessionID); id != "" {
		nativeID, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("qoder: invalid native session id: %w", err)
		}
		cmd = append(cmd, "--session-id", nativeID.String())
	} else if cfg.SessionID != "" {
		cmd = append(cmd, "--session-id", SessionUUID(cfg.SessionID))
	}

	permissions := cfg.Permissions
	if permissions == "" {
		permissions = cfg.Config.Permissions
	}
	appendPermissionFlags(&cmd, permissions)
	appendModelFlag(&cmd, cfg.Config.Model)
	appendToolFlags(&cmd, cfg.AllowedTools, cfg.DisallowedTools)

	systemPrompt, err := systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
	if err != nil {
		return nil, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}

	if cfg.Prompt != "" {
		cmd = append(cmd, "-i", cfg.Prompt)
	}
	return cmd, nil
}

// GetRestoreCommand rebuilds the argv that continues an existing Qoder CLI
// session: `qodercli [--permission-mode <mode>] [-m <model>] --resume
// <agentSessionId>`. It prefers the hook-captured native session id from
// cfg.Session.Metadata["agentSessionId"]; for sessions created before hooks
// captured it, it falls back to the deterministic UUID AO pins via
// --session-id at launch. ok is false only when neither is available, so the
// caller fresh-spawns. A present Prompt is passed via -i as the resume-time
// user turn.
func (p *Plugin) GetRestoreCommand(ctx context.Context, cfg ports.RestoreConfig) (cmd []string, ok bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	identity := strings.TrimSpace(cfg.Session.Metadata[ports.MetadataKeyAgentSessionID])
	if identity == "" && cfg.Session.ID != "" {
		identity = SessionUUID(cfg.Session.ID)
	}
	if identity == "" {
		return nil, false, nil
	}

	binary, err := p.qoderBinary(ctx)
	if err != nil {
		return nil, false, err
	}

	cmd = []string{binary}
	appendPermissionFlags(&cmd, cfg.Permissions)
	appendModelFlag(&cmd, cfg.Config.Model)
	appendToolFlags(&cmd, cfg.AllowedTools, cfg.DisallowedTools)
	systemPrompt, err := systemPromptTextFrom(cfg.SystemPrompt, cfg.SystemPromptFile)
	if err != nil {
		return nil, false, err
	}
	if systemPrompt != "" {
		cmd = append(cmd, "--append-system-prompt", systemPrompt)
	}
	cmd = append(cmd, "--resume", identity)
	if cfg.Prompt != "" {
		cmd = append(cmd, "-i", cfg.Prompt)
	}
	return cmd, true, nil
}

// NativeConversationID bridges Qoder's terminal session id and ACP session id:
// both surfaces share the same native session UUID.
func (p *Plugin) NativeConversationID(
	ctx context.Context,
	session ports.SessionRef,
	currentMode domain.SessionMode,
	providerConversationID string,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if currentMode == domain.SessionModeChat {
		id := strings.TrimSpace(providerConversationID)
		return id, id != "", nil
	}
	id := strings.TrimSpace(session.Metadata[ports.MetadataKeyAgentSessionID])
	if id == "" && session.ID != "" {
		id = SessionUUID(session.ID)
	}
	return id, id != "", nil
}

// NativeConversationExists reports whether a Qoder session UUID has a
// non-empty transcript. Qoder CLI stores transcripts under
// <configRoot>/projects/<project-key>/<session-id>.jsonl, where the config
// root is $QODER_CONFIG_DIR, ~/.qoder-cn for the China build, or ~/.qoder for
// the international build. AO only tests for existence — it never parses the
// file.
func (p *Plugin) NativeConversationExists(
	ctx context.Context,
	_ ports.SessionRef,
	nativeConversationID string,
	env map[string]string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id := strings.TrimSpace(nativeConversationID)
	if _, err := uuid.Parse(id); err != nil {
		return false, nil
	}
	roots, err := qoderConfigRoots(env)
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		exists, err := transcriptExists(ctx, root, id)
		if err != nil || exists {
			return exists, err
		}
	}
	return false, nil
}

// qoderConfigRoots returns the config roots to probe, in priority order: an
// explicit QODER_CONFIG_DIR (session env, then process env), then both
// well-known per-build roots.
func qoderConfigRoots(env map[string]string) ([]string, error) {
	var roots []string
	if dir := strings.TrimSpace(env["QODER_CONFIG_DIR"]); dir != "" {
		roots = append(roots, dir)
	}
	if dir := strings.TrimSpace(os.Getenv("QODER_CONFIG_DIR")); dir != "" && !containsString(roots, dir) {
		roots = append(roots, dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("qoder: resolve config roots: %w", err)
	}
	for _, dir := range []string{filepath.Join(home, ".qoder-cn"), filepath.Join(home, ".qoder")} {
		if !containsString(roots, dir) {
			roots = append(roots, dir)
		}
	}
	return roots, nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func transcriptExists(ctx context.Context, configRoot, id string) (bool, error) {
	projectsDir := filepath.Join(configRoot, "projects")
	projects, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("qoder: read transcript root %s: %w", projectsDir, err)
	}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !project.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(projectsDir, project.Name(), id+".jsonl"))
		switch {
		case err == nil && info.Mode().IsRegular() && info.Size() > 0:
			return true, nil
		case err == nil, os.IsNotExist(err):
			continue
		default:
			return false, fmt.Errorf("qoder: inspect transcript for %s: %w", id, err)
		}
	}
	return false, nil
}

// qoderSessionNamespace is a fixed UUIDv5 namespace, distinct from Claude
// Code's, mapping AO session ids onto stable Qoder session UUIDs.
var qoderSessionNamespace = uuid.MustParse("6f9d8c2a-4e1b-5a7d-9c3f-2b8e4d6a1f50")

// SessionUUID maps an AO session id onto the stable Qoder CLI session UUID
// used by --session-id and --resume.
func SessionUUID(aoSessionID string) string {
	return uuid.NewSHA1(qoderSessionNamespace, []byte(aoSessionID)).String()
}

// Qoder's append-system-prompt flag accepts inline text only. The manager
// normally supplies both inline text and an AO-owned file; if only the file is
// present, read it and pass the contents inline.
func systemPromptTextFrom(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	data, err := os.ReadFile(file) //nolint:gosec // path is AO-owned launch config
	if err != nil {
		return "", fmt.Errorf("qoder: read system prompt file: %w", err)
	}
	return string(data), nil
}

// SessionInfo surfaces Qoder hook-derived metadata. Metadata is intentionally
// nil: callers get the normalized fields directly.
func (p *Plugin) SessionInfo(ctx context.Context, session ports.SessionRef) (ports.SessionInfo, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.SessionInfo{}, false, err
	}
	info, ok := agentbase.StandardSessionInfo(session)
	return info, ok, nil
}

// qoderBinarySpec locates the Qoder CLI binary: PATH first, then the official
// installer's locations (~/.local/bin symlinks and the versioned stores). The
// China build (qoderclicn, config root ~/.qoder-cn, phone/Aliyun login) is
// preferred over the international build (qodercli, config root ~/.qoder,
// GitHub/Google login); whichever the user installed is the one AO drives.
var qoderBinarySpec = binaryutil.BinarySpec{
	Label:    "qoder",
	Names:    []string{"qoderclicn", "qodercli"},
	WinNames: []string{"qoderclicn.exe", "qoderclicn.cmd", "qoderclicn", "qodercli.exe", "qodercli.cmd", "qodercli"},
	UnixPaths: []string{
		"/usr/local/bin/qoderclicn", "/opt/homebrew/bin/qoderclicn",
		"/usr/local/bin/qodercli", "/opt/homebrew/bin/qodercli",
	},
	UnixHomePaths: [][]string{
		{".local", "bin", "qoderclicn"},
		{".qoder-cn", "bin", "qoderclicn", "qoderclicn"},
		{".local", "bin", "qodercli"},
		{".qoder", "bin", "qodercli", "qodercli"},
	},
}

// ResolveQoderBinary returns the path to the qodercli binary, or a wrapped
// ports.ErrAgentBinaryNotFound when it is absent.
func ResolveQoderBinary(ctx context.Context) (string, error) {
	return binaryutil.ResolveBinary(ctx, qoderBinarySpec)
}

// ResolveBinary implements nativeacp.Plugin and ports.AgentBinaryResolver.
func (p *Plugin) ResolveBinary(ctx context.Context) (string, error) {
	return p.qoderBinary(ctx)
}

func (p *Plugin) qoderBinary(ctx context.Context) (string, error) {
	p.binaryMu.Lock()
	defer p.binaryMu.Unlock()

	if p.resolvedBinary != "" {
		return p.resolvedBinary, nil
	}

	binary, err := ResolveQoderBinary(ctx)
	if err != nil {
		return "", err
	}
	p.resolvedBinary = binary
	return binary, nil
}

// InvalidateBinaryResolution makes the next operation resolve Qoder again, so
// a reinstall or upgrade that relocates the binary is picked up.
func (p *Plugin) InvalidateBinaryResolution() {
	p.binaryMu.Lock()
	p.resolvedBinary = ""
	p.binaryMu.Unlock()
}

// appendPermissionFlags maps AO's four permission modes onto Qoder CLI's
// `--permission-mode` choices (default|accept_edits|bypass_permissions|
// dont_ask|auto). Default emits no flag so Qoder resolves its starting mode
// from the user's own settings.
func appendPermissionFlags(cmd *[]string, permissions ports.PermissionMode) {
	switch ports.NormalizePermissionMode(permissions) {
	case ports.PermissionModeDefault:
		// No flag: defer to the user's Qoder settings/default behavior.
	case ports.PermissionModeAcceptEdits:
		*cmd = append(*cmd, "--permission-mode", "accept_edits")
	case ports.PermissionModeAuto:
		*cmd = append(*cmd, "--permission-mode", "auto")
	case ports.PermissionModeBypassPermissions:
		*cmd = append(*cmd, "--permission-mode", "bypass_permissions")
	}
}

// appendModelFlag appends -m if a non-empty model is configured.
func appendModelFlag(cmd *[]string, model string) {
	if model := strings.TrimSpace(model); model != "" {
		*cmd = append(*cmd, "-m", model)
	}
}

// appendToolFlags applies the tool allowlist/denylist. qodercli takes both as
// repeatable flags.
func appendToolFlags(cmd *[]string, allowed, disallowed []string) {
	for _, tool := range allowed {
		*cmd = append(*cmd, "--allowed-tools", tool)
	}
	for _, tool := range disallowed {
		*cmd = append(*cmd, "--disallowed-tools", tool)
	}
}

// AugmentRuntimeEnv neutralizes Qoder desktop Agent-SDK session variables
// before AO launches the CLI. When the daemon itself was started from inside a
// Qoder desktop session, the inherited QODER_AGENT_SDK_ENTRYPOINT forces the
// CLI into the private SDK stream-json mode, where it rejects ordinary flags
// such as --acp. The CLI treats an empty value as unset. Applied to both TUI
// (session manager) and Chat (nativeacp) launches; see the
// runtimeEnvAugmenter contract.
func (p *Plugin) AugmentRuntimeEnv(env map[string]string, _ string) {
	env["QODER_AGENT_SDK_ENTRYPOINT"] = ""
	env["QODER_SDK_AUTH_PAYLOAD_FILE"] = ""
}

// AppendSessionFlags adds the TUI-equivalent permission and model flags so
// Chat launches the same Qoder process the terminal adapter would, plus ACP.
func AppendSessionFlags(cmd *[]string, permissions ports.PermissionMode, model string) {
	appendPermissionFlags(cmd, permissions)
	appendModelFlag(cmd, model)
}
