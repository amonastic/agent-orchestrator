package qoderacp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/qoder"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Run explicitly with AO_PROBE_QODER_ACP=1. It uses the user's existing Qoder
// CLI executable, settings, and credentials; CI never depends on them.
func TestLiveQoderACPHandshake(t *testing.T) {
	if os.Getenv("AO_PROBE_QODER_ACP") != "1" {
		t.Skip("set AO_PROBE_QODER_ACP=1 to probe the local Qoder CLI install")
	}

	driver := New(qoder.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-qoder-acp-handshake", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		Env: envMap(), Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()

	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatal("controller closed before ready")
			}
			if event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady {
				return
			}
			if event.Kind == ports.ChatEventError {
				t.Fatalf("controller error: %v", event.Err)
			}
		case <-ctx.Done():
			t.Fatalf("handshake timed out: %v", ctx.Err())
		}
	}
}

// Run explicitly with AO_LIVE_QODER_ACP=1. It spends one minimal model turn
// against the local Qoder account.
func TestLiveQoderACP(t *testing.T) {
	if os.Getenv("AO_LIVE_QODER_ACP") != "1" {
		t.Skip("set AO_LIVE_QODER_ACP=1 to run against the local Qoder account")
	}

	driver := New(qoder.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := driver.Probe(ctx); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	conversation, err := driver.Start(ctx, ports.ChatStartConfig{
		SessionID: "live-qoder-acp", DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
		Env: envMap(), Permissions: ports.PermissionModeBypassPermissions,
		SystemPrompt: "Answer in one short sentence.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer conversation.Close()

	ref, err := conversation.SendTurn(ctx, ports.ChatUserMessage{
		Text: "Reply with exactly: AO Qoder ACP works", ClientMessageID: "live-1",
		Origin: domain.MessageOriginHuman,
	})
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	if err := conversation.(ports.ChatDeferredTurnStarter).StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatalf("StartDeferredTurn: %v", err)
	}

	var answer strings.Builder
	for {
		select {
		case event, ok := <-conversation.Events():
			if !ok {
				t.Fatalf("controller closed before completion; answer=%q", answer.String())
			}
			switch event.Kind {
			case ports.ChatEventMessageDelta:
				answer.WriteString(event.Delta)
			case ports.ChatEventTurnCompleted:
				if event.TurnState != domain.TurnStateCompleted {
					t.Fatalf("turn state = %q; answer=%q", event.TurnState, answer.String())
				}
				if !strings.Contains(answer.String(), "AO Qoder ACP works") {
					t.Fatalf("answer = %q", answer.String())
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("live turn timed out: %v; answer=%q", ctx.Err(), answer.String())
		}
	}
}

func envMap() map[string]string {
	out := make(map[string]string)
	for _, pair := range os.Environ() {
		name, value, ok := strings.Cut(pair, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}
