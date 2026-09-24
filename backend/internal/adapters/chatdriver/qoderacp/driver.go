// Package qoderacp binds the user's own Qoder CLI installation to AO's
// reusable ACP Chat transport.
package qoderacp

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qoder"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `qodercli --acp` from the binary resolved by the Qoder agent
// plugin. Login, models, settings, and updates stay with the user's install.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return newDriver(plugin, versionProbe, log)
}

func newDriver(plugin nativeacp.Plugin, probe nativeacp.VersionProbe, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:        domain.HarnessQoder,
		Configure:      configure,
		SessionOptions: sessionOptions,
		VersionProbe:   probe,
		Capabilities: ports.ChatCapabilities{
			// Qoder enforces permissions over ACP request_permission.
			ports.ChatCapabilityApprovals: true,
		},
	}, log)
}

// configure builds the `qodercli --acp` argv. Permissions ride along as the
// CLI's own --permission-mode flag rather than an ACP session mode: Qoder's
// ACP surface does not advertise a mode list in its initialize response, and
// an unverified session/set_mode id would fail session creation outright.
// The launch flag is the authoritative permission input; the ACP
// request_permission channel still parks approvals for interactive handling.
func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	qoder.AppendSessionFlags(&args, cfg.Permissions, cfg.Model)
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		args = append(args, "--append-system-prompt", prompt)
	}
	return args, nil, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}
