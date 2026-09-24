package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

var _ ports.AgentAuthChecker = (*Plugin)(nil)

// AuthStatus checks Qoder CLI's local authentication state without starting a
// session. It prefers the bounded `qodercli status -o json` probe, which
// reports {"logged_in": bool}; an unparseable result is Unknown, never a
// guess. An SDK auth payload file supplied by a host (the desktop app's
// QODER_SDK_AUTH_PAYLOAD_FILE) counts as definitive authorization when the
// file exists.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	binary, err := p.qoderBinary(ctx)
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if status, ok := qoderEnvAuthStatus(ctx); ok {
		return status, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := aoprocess.CommandContext(probeCtx, binary, "status", "-o", "json").CombinedOutput()
	if probeCtx.Err() != nil {
		return ports.AgentAuthStatusUnknown, probeCtx.Err()
	}
	if status, ok := qoderAuthStatusFromOutput(out); ok {
		return status, nil
	}
	// An unfamiliar non-zero result is not affirmative evidence of missing
	// credentials.
	_ = err
	return ports.AgentAuthStatusUnknown, nil
}

func qoderEnvAuthStatus(ctx context.Context) (ports.AgentAuthStatus, bool) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, false
	}
	if path := strings.TrimSpace(os.Getenv("QODER_SDK_AUTH_PAYLOAD_FILE")); path != "" {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			return ports.AgentAuthStatusAuthorized, true
		}
	}
	return ports.AgentAuthStatusUnknown, false
}

func qoderAuthStatusFromOutput(out []byte) (ports.AgentAuthStatus, bool) {
	start := bytes.IndexByte(out, '{')
	end := bytes.LastIndexByte(out, '}')
	if start < 0 || end < start {
		return ports.AgentAuthStatusUnknown, false
	}
	var status struct {
		LoggedIn *bool `json:"logged_in"`
	}
	if json.Unmarshal(out[start:end+1], &status) != nil {
		return ports.AgentAuthStatusUnknown, false
	}
	if status.LoggedIn == nil {
		return ports.AgentAuthStatusUnknown, false
	}
	if *status.LoggedIn {
		return ports.AgentAuthStatusAuthorized, true
	}
	return ports.AgentAuthStatusUnauthorized, true
}
