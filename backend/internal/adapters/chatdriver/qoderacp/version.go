package qoderacp

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// minimumQoderVersion is the oldest Qoder CLI build verified against AO's ACP
// transport: 1.1.62 answers initialize with protocolVersion 1, advertises
// loadSession/resume, and reports auth-required as JSON-RPC -32000. `--acp` is
// an official (help-hidden) flag of this build.
const minimumQoderVersion = "1.1.62"

var versionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func versionProbe(ctx context.Context, bin string) error {
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read Qoder CLI version: %w", err)
	}
	return validateVersionOutput(string(output))
}

func validateVersionOutput(output string) error {
	installed, ok := parseVersion(output)
	if !ok {
		return fmt.Errorf("unrecognized Qoder CLI version %q (AO requires %s or newer)",
			strings.TrimSpace(output), minimumQoderVersion)
	}
	minimum, _ := parseVersion(minimumQoderVersion)
	if installed.less(minimum) {
		return fmt.Errorf("installed Qoder CLI %s is older than AO's tested minimum %s",
			installed, minimumQoderVersion)
	}
	return nil
}

type version [3]int

func parseVersion(output string) (version, bool) {
	match := versionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return version{}, false
	}
	var parsed version
	for i := range parsed {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return version{}, false
		}
		parsed[i] = value
	}
	return parsed, true
}

func (v version) less(other version) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v version) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
