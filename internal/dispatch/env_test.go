package dispatch

import (
	"testing"

	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

func TestRunModelPropagatesMoxieJobIDEnv(t *testing.T) {
	client := &oneagent.Client{
		Backends: map[string]oneagent.Backend{
			"envtest": {
				Cmd:    []string{"sh", "-c", `printf '{"result":"%s"}' "$MOXIE_JOB_ID"`},
				Format: "json",
				Result: "result",
			},
		},
	}

	job := &store.PendingJob{
		ID:    "job-env-1",
		State: store.State{Backend: "envtest"},
	}

	result, interrupted := RunModel(job, client, nil)
	if interrupted {
		t.Fatal("RunModel reported interruption")
	}
	if result != "job-env-1" {
		t.Fatalf("result = %q, want job-env-1", result)
	}
}
