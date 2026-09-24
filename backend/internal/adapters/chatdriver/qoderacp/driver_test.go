package qoderacp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesNativeACP(t *testing.T) {
	tests := []struct {
		name string
		cfg  acpdriver.LaunchConfig
		want []string
	}{
		{name: "defaults", want: []string{"--acp"}},
		{
			name: "model prompt and accept-edits approvals",
			cfg: acpdriver.LaunchConfig{
				Model: "qwen3-coder-flash", SystemPrompt: "Follow AO rules.",
				Permissions: ports.PermissionModeAcceptEdits,
			},
			want: []string{"--acp", "--permission-mode", "accept_edits", "-m", "qwen3-coder-flash", "--append-system-prompt", "Follow AO rules."},
		},
		{
			name: "auto uses auto permissions",
			cfg:  acpdriver.LaunchConfig{Permissions: ports.PermissionModeAuto},
			want: []string{"--acp", "--permission-mode", "auto"},
		},
		{
			name: "bypass uses bypass_permissions",
			cfg:  acpdriver.LaunchConfig{Permissions: ports.PermissionModeBypassPermissions},
			want: []string{"--acp", "--permission-mode", "bypass_permissions"},
		},
		{
			name: "blank system prompt is dropped",
			cfg:  acpdriver.LaunchConfig{SystemPrompt: "   "},
			want: []string{"--acp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, env, err := configure(context.Background(), tt.cfg)
			if err != nil {
				t.Fatalf("configure: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) || env != nil {
				t.Fatalf("args/env = %#v, %#v; want %#v, nil", got, env, tt.want)
			}
		})
	}
}

func TestSessionOptionsUseModelOption(t *testing.T) {
	if got := sessionOptions(ports.ChatTurnSettings{}); got != nil {
		t.Fatalf("empty settings = %#v", got)
	}
	got := sessionOptions(ports.ChatTurnSettings{Model: "qwen3-coder-flash"})
	want := []acpdriver.SessionOption{{ID: "model", Value: "qwen3-coder-flash"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %#v, want %#v", got, want)
	}
}

func TestDriverReusesQoderPluginForProbe(t *testing.T) {
	plugin := &fakePlugin{status: ports.AgentAuthStatusAuthorized, binary: "/usr/bin/qodercli"}
	versionCalls := 0
	driver := newDriver(plugin, func(_ context.Context, bin string) error {
		versionCalls++
		if bin != "/usr/bin/qodercli" {
			t.Fatalf("version probe binary = %q, want /usr/bin/qodercli", bin)
		}
		return nil
	}, nil)

	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if driver.Harness() != domain.HarnessQoder {
		t.Fatalf("harness = %q", driver.Harness())
	}
	for _, capability := range []ports.ChatCapability{
		ports.ChatCapabilityStreaming, ports.ChatCapabilityTools,
		ports.ChatCapabilityInterrupt, ports.ChatCapabilityResume,
		ports.ChatCapabilityApprovals,
	} {
		if !caps.Has(capability) {
			t.Errorf("missing capability %q", capability)
		}
	}
	if plugin.resolveCalls != 1 || plugin.authCalls != 1 || versionCalls != 1 {
		t.Fatalf("plugin calls = resolve %d, auth %d, version %d; want one each",
			plugin.resolveCalls, plugin.authCalls, versionCalls)
	}
}

func TestDriverRejectsUnauthenticatedQoder(t *testing.T) {
	driver := newDriver(
		&fakePlugin{status: ports.AgentAuthStatusUnauthorized, binary: "/usr/bin/qodercli"},
		func(context.Context, string) error { return nil },
		nil,
	)
	if _, err := driver.Probe(context.Background()); !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("Probe error = %v, want ErrChatAuthRequired", err)
	}
}

func TestDriverAdmitsAllPermissionModes(t *testing.T) {
	driver := newDriver(
		&fakePlugin{status: ports.AgentAuthStatusAuthorized, binary: "/usr/bin/qodercli"},
		func(context.Context, string) error { return nil },
		nil,
	)
	caps, err := driver.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if missing := ports.MissingProductionCapabilities(caps); len(missing) != 0 {
		t.Fatalf("production floor gap = %v, want none", missing)
	}
	for _, permission := range []ports.PermissionMode{
		ports.PermissionModeDefault,
		ports.PermissionModeAcceptEdits,
		ports.PermissionModeAuto,
		ports.PermissionModeBypassPermissions,
	} {
		if missing := ports.MissingCapabilitiesForPermissions(caps, permission); len(missing) != 0 {
			t.Fatalf("permission %q missing=%v, want none", permission, missing)
		}
	}
}

func TestValidateVersionOutput(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "tested version", output: "1.1.62\n"},
		{name: "newer patch", output: "1.1.63"},
		{name: "newer major", output: "2.0.0"},
		{name: "version with prefix", output: "qodercli/1.2.0 darwin-arm64"},
		{name: "older version", output: "1.0.9", wantErr: true},
		{name: "much older", output: "0.9.99", wantErr: true},
		{name: "unparseable", output: "unknown", wantErr: true},
		{name: "empty", output: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVersionOutput(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateVersionOutput(%q) err = %v, wantErr %v", tt.output, err, tt.wantErr)
			}
		})
	}
}

type fakePlugin struct {
	binary       string
	status       ports.AgentAuthStatus
	resolveCalls int
	authCalls    int
}

func (p *fakePlugin) ResolveBinary(context.Context) (string, error) {
	p.resolveCalls++
	return p.binary, nil
}

func (p *fakePlugin) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	p.authCalls++
	return p.status, nil
}
