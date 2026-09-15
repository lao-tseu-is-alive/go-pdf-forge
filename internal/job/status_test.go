package job

import "testing"

func TestValidateTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current Status
		next    Status
		wantErr bool
	}{
		{name: "claim", current: StatusQueued, next: StatusAnalyzing},
		{name: "input already meets target", current: StatusAnalyzing, next: StatusCompleted},
		{name: "validate another profile", current: StatusValidating, next: StatusOptimizing},
		{name: "expire result", current: StatusCompleted, next: StatusExpired},
		{name: "cannot resurrect", current: StatusExpired, next: StatusQueued, wantErr: true},
		{name: "cannot skip analysis", current: StatusQueued, next: StatusOptimizing, wantErr: true},
		{name: "invalid status", current: Status("unknown"), next: StatusFailed, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTransition(tt.current, tt.next)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTransition(%q, %q) error = %v, wantErr %v", tt.current, tt.next, err, tt.wantErr)
			}
		})
	}
}

func TestTerminal(t *testing.T) {
	t.Parallel()

	for _, status := range []Status{StatusCompleted, StatusFailed, StatusCancelled, StatusExpired} {
		if !status.Terminal() {
			t.Errorf("%q should be terminal", status)
		}
	}
	if StatusOptimizing.Terminal() {
		t.Error("optimizing should not be terminal")
	}
}
