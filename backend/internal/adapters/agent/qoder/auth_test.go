package qoder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestQoderAuthStatusFromOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   ports.AgentAuthStatus
		wantOK bool
	}{
		{
			name:   "logged in",
			output: "{\n  \"logged_in\": true,\n  \"version\": \"1.1.62\"\n}",
			want:   ports.AgentAuthStatusAuthorized,
			wantOK: true,
		},
		{
			name:   "logged out",
			output: `{"logged_in": false, "version": "1.1.62", "allow_byok": 0}`,
			want:   ports.AgentAuthStatusUnauthorized,
			wantOK: true,
		},
		{
			name:   "json embedded in log noise",
			output: "warning: something\n{\"logged_in\": true}\ntrailing",
			want:   ports.AgentAuthStatusAuthorized,
			wantOK: true,
		},
		{
			name:   "missing logged_in field",
			output: `{"version": "1.1.62"}`,
			want:   ports.AgentAuthStatusUnknown,
			wantOK: false,
		},
		{
			name:   "unparseable output",
			output: "qodercli: command not found",
			want:   ports.AgentAuthStatusUnknown,
			wantOK: false,
		},
		{
			name:   "empty output",
			output: "",
			want:   ports.AgentAuthStatusUnknown,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := qoderAuthStatusFromOutput([]byte(tt.output))
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("qoderAuthStatusFromOutput(%q) = (%q, %v), want (%q, %v)",
					tt.output, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestQoderEnvAuthStatus(t *testing.T) {
	t.Run("unset is unknown", func(t *testing.T) {
		t.Setenv("QODER_SDK_AUTH_PAYLOAD_FILE", "")
		status, ok := qoderEnvAuthStatus(t.Context())
		if ok || status != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = (%q, %v), want (unknown, false)", status, ok)
		}
	})

	t.Run("dangling path is unknown", func(t *testing.T) {
		t.Setenv("QODER_SDK_AUTH_PAYLOAD_FILE", filepath.Join(t.TempDir(), "missing.json"))
		status, ok := qoderEnvAuthStatus(t.Context())
		if ok || status != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = (%q, %v), want (unknown, false)", status, ok)
		}
	})

	t.Run("existing non-empty payload authorizes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "payload.json")
		if err := os.WriteFile(path, []byte(`{"token":"redacted"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("QODER_SDK_AUTH_PAYLOAD_FILE", path)
		status, ok := qoderEnvAuthStatus(t.Context())
		if !ok || status != ports.AgentAuthStatusAuthorized {
			t.Fatalf("status = (%q, %v), want (authorized, true)", status, ok)
		}
	})

	t.Run("empty payload file is unknown", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "payload.json")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("QODER_SDK_AUTH_PAYLOAD_FILE", path)
		status, ok := qoderEnvAuthStatus(t.Context())
		if ok || status != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = (%q, %v), want (unknown, false)", status, ok)
		}
	})
}
