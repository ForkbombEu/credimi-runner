package runtime

import "testing"

func TestBuildRuntimePlanExpectedServices(t *testing.T) {
	tests := []struct {
		name string
		vals Values
		want []string
	}{
		{"container-auto", Values{}, []string{"runner", "caddy", "tunnel", "temporal"}},
		{"container-managed", Values{"CREDIMI_SERVICE_MODE": "cloudflare-managed"}, []string{"runner", "caddy", "tunnel_named", "temporal"}},
		{"host-manual", Values{"CREDIMI_RUNNER_BACKEND": "host", "CREDIMI_SERVICE_MODE": "manual"}, []string{"runner_host_process", "temporal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := NormalizeValues(tt.vals, "linux")
			if err != nil {
				t.Fatal(err)
			}
			plan := BuildRuntimePlan("/tmp/credimi", normalized)
			var got []string
			for _, service := range plan.ExpectedServices {
				got = append(got, service.ID)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ExpectedServices = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ExpectedServices = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestDiffValuesCoverageBranches(t *testing.T) {
	if got := DiffValues(Values{"RUNNER_IMAGE": "a"}, Values{"RUNNER_IMAGE": "a"}); len(got.Classes) != 1 || got.Classes[0] != ApplySavedOnly {
		t.Fatalf("saved only diff = %#v", got)
	}
	if got := DiffValues(Values{"CREDIMI_RUNNER_NAME": "a"}, Values{"CREDIMI_RUNNER_NAME": "b"}); len(got.ChangedKeys) == 0 || !containsApplyClass(got.Classes, ApplyRestartRequired) || !containsApplyClass(got.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("runner name changed diff = %#v", got)
	}
	if got := DiffValues(Values{"RUNNER_PORT": "8050"}, Values{"RUNNER_PORT": "8051"}); !containsApplyClass(got.Classes, ApplyComposeRecreate) || !containsApplyClass(got.Classes, ApplyCredimiUpdateRequired) {
		t.Fatalf("recreate diff = %#v", got)
	}
}

func containsApplyClass(classes []ApplyClass, want ApplyClass) bool {
	for _, class := range classes {
		if class == want {
			return true
		}
	}
	return false
}
