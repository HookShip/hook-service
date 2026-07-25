package backend

import (
	"fmt"
	"strings"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/runtime"
)

// DefaultEvaluationSinkURL is the loopback sink the evaluation profile delivers
// to when none is configured.
const DefaultEvaluationSinkURL = "http://127.0.0.1:9099/sink"

// ResolveAssignments loads the assignment/credential source for a profile. A
// configured file wins; otherwise the evaluation memory profile synthesizes the
// well-known local evaluation assignment.
func ResolveAssignments(runtimeCfg config.RuntimeConfig) ([]runtime.Assignment, error) {
	if runtimeCfg.ConfigFile != "" {
		file, err := assignmentconfig.Load(runtimeCfg.ConfigFile)
		if err != nil {
			return nil, err
		}
		return file.RuntimeAssignments()
	}
	if runtimeCfg.Backend == capabilities.ProfileMemory && runtimeCfg.EvaluationMode {
		sink := strings.TrimSpace(runtimeCfg.Sink.SinkURL)
		if sink == "" {
			sink = DefaultEvaluationSinkURL
		}
		return []runtime.Assignment{assignmentconfig.DefaultEvaluationAssignment(sink)}, nil
	}
	return nil, fmt.Errorf("profile %q requires an assignment config file (HOOKSHIP_CONFIG_FILE)", runtimeCfg.Backend)
}
