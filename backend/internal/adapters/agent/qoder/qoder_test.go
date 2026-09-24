package qoder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hooksjson"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetLaunchCommandBuildsArgv(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions:  ports.PermissionModeBypassPermissions,
		Prompt:       "-fix this",
		SystemPrompt: "be terse",
		Config:       domain.AgentConfig{Model: " qwen3-coder-flash "},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"qodercli",
		"--permission-mode", "bypass_permissions",
		"-m", "qwen3-coder-flash",
		"--append-system-prompt", "be terse",
		"-i", "-fix this",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected command\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetLaunchCommandPinsDeterministicSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID: "ao-session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--session-id", SessionUUID("ao-session-1")}) {
		t.Fatalf("command %#v missing deterministic --session-id", cmd)
	}

	// A caller-assigned native id wins over the derived fallback.
	native := "f194dbbc-f28a-4449-b885-09dcec9b5b7f"
	cmd, err = plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID:       "ao-session-1",
		NativeSessionID: native,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--session-id", native}) {
		t.Fatalf("command %#v missing native --session-id %s", cmd, native)
	}

	// An invalid native id is rejected, not silently dropped.
	if _, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		NativeSessionID: "not-a-uuid",
	}); err == nil {
		t.Fatal("expected error for invalid native session id")
	}
}

func TestSessionUUIDIsStableAndDistinct(t *testing.T) {
	first := SessionUUID("ao-session-1")
	if first != SessionUUID("ao-session-1") {
		t.Fatal("SessionUUID is not deterministic")
	}
	if first == SessionUUID("ao-session-2") {
		t.Fatal("SessionUUID collides across AO sessions")
	}
	if len(first) != 36 {
		t.Fatalf("SessionUUID = %q, want UUID shape", first)
	}
}

func TestGetLaunchCommandMapsPermissionModes(t *testing.T) {
	tests := []struct {
		name        string
		permission  ports.PermissionMode
		want        []string
		notExpected string
	}{
		{
			name:        "default",
			permission:  ports.PermissionModeDefault,
			notExpected: "--permission-mode",
		},
		{
			name:       "accept-edits",
			permission: ports.PermissionModeAcceptEdits,
			want:       []string{"--permission-mode", "accept_edits"},
		},
		{
			name:       "auto",
			permission: ports.PermissionModeAuto,
			want:       []string{"--permission-mode", "auto"},
		},
		{
			name:       "bypass-permissions",
			permission: ports.PermissionModeBypassPermissions,
			want:       []string{"--permission-mode", "bypass_permissions"},
		},
		{
			name:        "empty falls back to default",
			permission:  "",
			notExpected: "--permission-mode",
		},
		{
			name:        "unknown falls back to default",
			permission:  "bogus",
			notExpected: "--permission-mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &Plugin{resolvedBinary: "qodercli"}
			cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
				Permissions: tt.permission,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.want) > 0 && !containsSubsequence(cmd, tt.want) {
				t.Fatalf("command %#v does not contain %#v", cmd, tt.want)
			}
			if tt.notExpected != "" && contains(cmd, tt.notExpected) {
				t.Fatalf("command %#v contains %q", cmd, tt.notExpected)
			}
		})
	}
}

func TestGetLaunchCommandProjectPermissionsFallback(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	// Stored project config drives the mode when the spawn carries none.
	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: domain.AgentConfig{Permissions: ports.PermissionModeAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--permission-mode", "auto"}) {
		t.Fatalf("command %#v missing project permission mode", cmd)
	}

	// An explicit per-spawn permission wins over the stored project default.
	cmd, err = plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Permissions: ports.PermissionModeAcceptEdits,
		Config:      domain.AgentConfig{Permissions: ports.PermissionModeAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--permission-mode", "accept_edits"}) {
		t.Fatalf("command %#v missing spawn permission override", cmd)
	}
}

func TestGetLaunchCommandOmitsBlankConfiguredModel(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Config: domain.AgentConfig{Model: "  "},
		Prompt: "fix it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(cmd, "-m") {
		t.Fatalf("command %#v contains unexpected -m flag", cmd)
	}
}

func TestGetLaunchCommandReadsSystemPromptFile(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
		Prompt:           "do it",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--append-system-prompt", "file instructions\n"}) {
		t.Fatalf("command %#v missing inlined system prompt file", cmd)
	}

	// A missing file is an error, not a silent drop.
	if _, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: filepath.Join(dir, "missing.md"),
	}); err == nil {
		t.Fatal("expected error for missing system prompt file")
	}
}

func TestGetLaunchCommandAppliesToolRestrictions(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		AllowedTools:    []string{"Read", "Grep"},
		DisallowedTools: []string{"Bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubsequence(cmd, []string{"--allowed-tools", "Read"}) ||
		!containsSubsequence(cmd, []string{"--allowed-tools", "Grep"}) ||
		!containsSubsequence(cmd, []string{"--disallowed-tools", "Bash"}) {
		t.Fatalf("command %#v missing tool restriction flags", cmd)
	}
}

func TestGetRestoreCommandReadsAgentSessionID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Permissions:  ports.PermissionModeAuto,
		Config:       domain.AgentConfig{Model: "qwen3-coder-plus"},
		SystemPrompt: "restore instructions",
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"qodercli",
		"--permission-mode", "auto",
		"-m", "qwen3-coder-plus",
		"--append-system-prompt", "restore instructions",
		"--resume", "sess-123",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("restore cmd\nwant: %#v\n got: %#v", want, cmd)
	}
}

func TestGetRestoreCommandFallsBackToPinnedUUID(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{ID: "ao-session-9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !containsSubsequence(cmd, []string{"--resume", SessionUUID("ao-session-9")}) {
		t.Fatalf("restore cmd %#v missing deterministic resume id", cmd)
	}
}

func TestGetRestoreCommandAppendsResumePrompt(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Prompt: "continue the fix",
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "sess-123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if !containsSubsequence(cmd, []string{"--resume", "sess-123", "-i", "continue the fix"}) {
		t.Fatalf("restore cmd %#v missing resume-time prompt", cmd)
	}
}

func TestGetRestoreCommandFalseWithoutAnyIdentity(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	cmd, ok, err := plugin.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "   "}},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	if cmd != nil {
		t.Fatalf("cmd = %#v, want nil", cmd)
	}
}

func TestGetPromptDeliveryStrategy(t *testing.T) {
	plugin := &Plugin{}

	got, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{Kind: domain.KindWorker, Prompt: "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("worker strategy = %q, want %q", got, ports.PromptDeliveryInCommand)
	}
}

func TestGetConfigSpecReportsModelAndPermissions(t *testing.T) {
	plugin := &Plugin{}

	spec, err := plugin.GetConfigSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 2 {
		t.Fatalf("config fields = %#v, want model + permissions", spec.Fields)
	}
	if spec.Fields[0].Key != "model" || spec.Fields[0].Type != ports.ConfigFieldString {
		t.Fatalf("first field = %#v, want model string", spec.Fields[0])
	}
	if spec.Fields[1].Key != "permissions" || spec.Fields[1].Type != ports.ConfigFieldEnum {
		t.Fatalf("second field = %#v, want permissions enum", spec.Fields[1])
	}
	if !reflect.DeepEqual(spec.Fields[1].Enum, permissionConfigEnum) {
		t.Fatalf("permissions enum = %#v", spec.Fields[1].Enum)
	}
}

func TestSessionInfoReadsHookMetadata(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}

	info, ok, err := plugin.SessionInfo(context.Background(), ports.SessionRef{
		WorkspacePath: "/some/path",
		Metadata: map[string]string{
			ports.MetadataKeyAgentSessionID: "sess-123",
			ports.MetadataKeyTitle:          "Fix login redirect",
			ports.MetadataKeySummary:        "Updated the auth callback and tests.",
			"ignored":                       "not returned",
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if info.AgentSessionID != "sess-123" || info.Title != "Fix login redirect" ||
		info.Summary != "Updated the auth callback and tests." {
		t.Fatalf("info = %#v", info)
	}
	if info.Metadata != nil {
		t.Fatalf("Metadata = %#v, want nil", info.Metadata)
	}

	_, ok, err = plugin.SessionInfo(context.Background(), ports.SessionRef{Metadata: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("ok = true for empty metadata, want false")
	}
}

type qoderHookFile struct {
	Hooks map[string][]hooksjson.MatcherGroup `json:"hooks"`
}

func TestGetAgentHooksInstallsQoderHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}
	workspace := t.TempDir()
	settingsDir := filepath.Join(workspace, ".qoder")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.local.json")
	// Pre-seed an unrelated top-level setting and a user-owned Stop hook; both
	// must be preserved.
	existing := `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ports.WorkspaceHookConfig{
		DataDir:       t.TempDir(),
		SessionID:     "sess-1",
		WorkspacePath: workspace,
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	// A second install must not duplicate AO hook commands.
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["theme"]) != `"dark"` {
		t.Fatalf("unrelated top-level setting not preserved: %s", top["theme"])
	}

	var config qoderHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.Hooks == nil {
		t.Fatalf("hooks config missing hooks object: %#v", config)
	}
	for _, spec := range qoderManagedHooks {
		entries := config.Hooks[spec.Event]
		if count := countQoderHookCommand(entries, spec.Command); count != 1 {
			t.Fatalf("%s command count = %d, want 1 in %#v", spec.Event, count, entries)
		}
	}
	if countQoderHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("existing Stop hook was not preserved: %#v", config.Hooks["Stop"])
	}
	assertSessionStartMatcher(t, config.Hooks["SessionStart"])
}

func assertSessionStartMatcher(t *testing.T, groups []hooksjson.MatcherGroup) {
	t.Helper()
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == qoderHookCommandPrefix+"session-start" {
				if group.Matcher == nil || *group.Matcher != "startup|resume|clear|compact" {
					t.Fatalf("session-start hook not under startup/resume matcher: %#v", group)
				}
				return
			}
		}
	}
	t.Fatalf("session-start hook not found: %#v", groups)
}

func TestUninstallHooksRemovesQoderHooks(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}
	workspace := t.TempDir()
	settingsPath := filepath.Join(workspace, ".qoder", "settings.local.json")

	ctx := context.Background()
	cfg := ports.WorkspaceHookConfig{DataDir: t.TempDir(), SessionID: "sess-1", WorkspacePath: workspace}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"custom stop hook","timeout":3}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := plugin.GetAgentHooks(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || !installed {
		t.Fatalf("AreHooksInstalled after install = (%v, %v), want (true, nil)", installed, err)
	}

	if err := plugin.UninstallHooks(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if installed, err := plugin.AreHooksInstalled(ctx, workspace); err != nil || installed {
		t.Fatalf("AreHooksInstalled after uninstall = (%v, %v), want (false, nil)", installed, err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config qoderHookFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range qoderManagedHooks {
		if got := countQoderHookCommand(config.Hooks[spec.Event], spec.Command); got != 0 {
			t.Fatalf("%s command %q count = %d after uninstall, want 0", spec.Event, spec.Command, got)
		}
	}
	if countQoderHookCommand(config.Hooks["Stop"], "custom stop hook") != 1 {
		t.Fatalf("user Stop hook not preserved: %#v", config.Hooks["Stop"])
	}
}

func TestNativeConversationIDBridgesTUIAndChat(t *testing.T) {
	p := &Plugin{}
	native := "f194dbbc-f28a-4449-b885-09dcec9b5b7f"

	// TUI without hook metadata falls back to the pinned deterministic UUID.
	id, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{
		ID: "ao-session-1", Metadata: map[string]string{},
	}, domain.SessionModeTUI, "")
	if err != nil || !ok || id != SessionUUID("ao-session-1") {
		t.Fatalf("derived TUI native id = %q ok=%v err=%v", id, ok, err)
	}

	// A hook-captured id wins over the derived fallback.
	id, ok, err = p.NativeConversationID(context.Background(), ports.SessionRef{
		ID:       "ao-session-1",
		Metadata: map[string]string{ports.MetadataKeyAgentSessionID: native},
	}, domain.SessionModeTUI, "")
	if err != nil || !ok || id != native {
		t.Fatalf("captured TUI native id = %q ok=%v err=%v", id, ok, err)
	}

	// Chat mode uses the provider conversation id.
	chatID, ok, err := p.NativeConversationID(context.Background(), ports.SessionRef{},
		domain.SessionModeChat, native)
	if err != nil || !ok || chatID != native {
		t.Fatalf("Chat native id = %q ok=%v err=%v", chatID, ok, err)
	}
}

func TestNativeConversationExistsRequiresPersistedTranscript(t *testing.T) {
	p := &Plugin{}
	id := "f194dbbc-f28a-4449-b885-09dcec9b5b7f"
	configDir := t.TempDir()
	env := map[string]string{"QODER_CONFIG_DIR": configDir}

	exists, err := p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("reserved session without a transcript reported as persisted")
	}

	projectDir := filepath.Join(configDir, "projects", "-test-proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projectDir, id+".jsonl")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || exists {
		t.Fatalf("empty transcript: exists=%v err=%v", exists, err)
	}
	if err := os.WriteFile(transcript, []byte("{\"sessionId\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, id, env)
	if err != nil || !exists {
		t.Fatalf("persisted transcript: exists=%v err=%v", exists, err)
	}

	exists, err = p.NativeConversationExists(context.Background(), ports.SessionRef{}, "not-a-uuid", env)
	if err != nil || exists {
		t.Fatalf("non-UUID id: exists=%v err=%v", exists, err)
	}
}

func TestInvalidateBinaryResolutionClearsCachedPath(t *testing.T) {
	p := &Plugin{resolvedBinary: "old-qodercli"}

	p.InvalidateBinaryResolution()

	if p.resolvedBinary != "" {
		t.Fatalf("resolvedBinary = %q, want empty after invalidation", p.resolvedBinary)
	}
}

func TestContextCancellationIsHonored(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "qodercli"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := plugin.GetConfigSpec(ctx); err == nil {
		t.Fatal("GetConfigSpec: want error from cancelled context")
	}
	if _, err := plugin.GetPromptDeliveryStrategy(ctx, ports.LaunchConfig{}); err == nil {
		t.Fatal("GetPromptDeliveryStrategy: want error from cancelled context")
	}
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{WorkspacePath: t.TempDir()}); err == nil {
		t.Fatal("GetAgentHooks: want error from cancelled context")
	}
	if err := plugin.UninstallHooks(ctx, t.TempDir()); err == nil {
		t.Fatal("UninstallHooks: want error from cancelled context")
	}
	if _, err := plugin.AreHooksInstalled(ctx, t.TempDir()); err == nil {
		t.Fatal("AreHooksInstalled: want error from cancelled context")
	}
	if _, _, err := plugin.GetRestoreCommand(ctx, ports.RestoreConfig{}); err == nil {
		t.Fatal("GetRestoreCommand: want error from cancelled context")
	}
	if _, _, err := plugin.SessionInfo(ctx, ports.SessionRef{}); err == nil {
		t.Fatal("SessionInfo: want error from cancelled context")
	}
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func containsSubsequence(values []string, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	for start := range values {
		if start+len(needle) > len(values) {
			return false
		}
		ok := true
		for offset, want := range needle {
			if values[start+offset] != want {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func countQoderHookCommand(entries []hooksjson.MatcherGroup, command string) int {
	count := 0
	for _, entry := range entries {
		for _, hook := range entry.Hooks {
			if strings.TrimSpace(hook.Command) == command {
				count++
			}
		}
	}
	return count
}
