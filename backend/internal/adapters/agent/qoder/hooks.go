package qoder

import (
	"context"
	"path/filepath"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	qoderSettingsDirName = ".qoder"
	// qoderSettingsFileName is the workspace-local file AO writes. Qoder CLI
	// reads both .qoder/settings.json and .qoder/settings.local.json in a
	// project; the local file is the gitignored one, matching Claude Code's
	// settings.local.json convention AO already follows.
	qoderSettingsFileName = "settings.local.json"

	// qoderHookCommandPrefix identifies the hook commands AO owns, so install
	// skips duplicates and uninstall recognizes AO entries by prefix.
	qoderHookCommandPrefix = "ao hooks qoder "

	// qoderHookTimeout is in seconds: Qoder CLI's hook system is
	// Claude-Code-shaped (it ships `qodercli hooks migrate` to import Claude
	// Code hooks), and Claude Code measures hook timeouts in seconds.
	qoderHookTimeout = 30
)

// qoderSessionStartMatcher is referenced by pointer so SessionStart serializes
// with the startup/resume source matcher, matching Claude Code's vocabulary.
var qoderSessionStartMatcher = "startup|resume|clear|compact"

// qoderManagedHooks is the source of truth for the hooks AO installs:
// SessionStart (under the startup/resume source matcher), UserPromptSubmit,
// PermissionRequest, and Stop. This conservative set covers identity capture
// and the activity transitions AO derives via StandardDeriveActivityState.
// The Claude-style PreToolUse/PostToolUse trio (finer blocked-state
// correlation) is deliberately not installed until Qoder's hook payloads are
// verified to carry the same tool_use_id fields.
var qoderManagedHooks = []hooksjson.HookSpec{
	{Event: "SessionStart", Matcher: &qoderSessionStartMatcher, Command: qoderHookCommandPrefix + "session-start"},
	{Event: "UserPromptSubmit", Command: qoderHookCommandPrefix + "user-prompt-submit"},
	{Event: "PermissionRequest", Command: qoderHookCommandPrefix + "permission-request"},
	{Event: "Stop", Command: qoderHookCommandPrefix + "stop"},
}

// qoderHooks manages AO's hooks in the workspace-local
// .qoder/settings.local.json file.
var qoderHooks = hooksjson.Manager{
	Label:         "qoder",
	CommandPrefix: qoderHookCommandPrefix,
	Timeout:       qoderHookTimeout,
	Path:          qoderSettingsPath,
	Managed:       qoderManagedHooks,
}

func qoderSettingsPath(workspacePath string) string {
	return filepath.Join(workspacePath, qoderSettingsDirName, qoderSettingsFileName)
}

// GetAgentHooks installs AO's Qoder hooks, preserving user-defined hooks and unrelated settings.
func (p *Plugin) GetAgentHooks(ctx context.Context, cfg ports.WorkspaceHookConfig) error {
	return qoderHooks.Install(ctx, cfg.WorkspacePath)
}

// UninstallHooks removes AO's Qoder hooks, leaving user-defined hooks untouched.
func (p *Plugin) UninstallHooks(ctx context.Context, workspacePath string) error {
	return qoderHooks.Uninstall(ctx, workspacePath)
}

// AreHooksInstalled reports whether any AO Qoder hook is present.
func (p *Plugin) AreHooksInstalled(ctx context.Context, workspacePath string) (bool, error) {
	return qoderHooks.AreInstalled(ctx, workspacePath)
}
