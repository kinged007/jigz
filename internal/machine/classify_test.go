package machine

import "testing"

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       FailureClass
		retryable  bool
	}{
		{"auth api key", "Invalid API key · Please run /login", FailureAuth, false},
		{"auth oauth expired", "OAuth token has expired. authentication_error", FailureAuth, false},
		{"auth 401", "unexpected status 401 Unauthorized: Missing bearer or basic authentication in header", FailureAuth, false},
		{"auth provider error", "Error: ProviderAuthError: authentication failed", FailureAuth, false},
		{"auth not logged in", "Not logged in · Please run /login", FailureAuth, false},
		{"cli version refused", "OpenCode 1.18.29 is not supported; install OpenCode 2.x, published on npm as @opencode/cli", FailureCLIVersion, false},
		{"cli no version", "the opencode command reported no version that can be read: usage: opencode", FailureCLIVersion, false},
		{"cli missing", "agent command not found: exec: \"foo\": executable file not found in $PATH", FailureCLIVersion, false},
		{"model unknown", "model not found: claude-sonnet-9", FailureModel, false},
		{"model none configured", "Error: no model configured", FailureModel, false},
		{"model none available", "No models available. Set an API key first.", FailureModel, false},
		{"limit wins over auth", "credential_unverified: You've hit your 5-hour limit · resets 1:10am (UTC) Invalid API key", FailureLimit, true},
		{"limit plain", "error: usage limit reached", FailureLimit, true},
		{"crash panic", "panic: runtime error: index out of range", FailureCrash, true},
		{"crash oom", "fatal error: out of memory", FailureCrash, true},
		{"transient timeout", "request failed: deadline exceeded", FailureTransient, true},
		{"transient reset", "connection reset by peer", FailureTransient, true},
		{"transient 503", "upstream returned 503 Service Unavailable", FailureTransient, true},
		{"unknown prose", "the linter reported 3 errors", FailureUnknown, true},
		{"unknown empty", "", FailureUnknown, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.text); got != c.want {
				t.Errorf("Classify = %q, want %q", got, c.want)
			}
			if got := Retryable(c.want); got != c.retryable {
				t.Errorf("Retryable(%q) = %v, want %v", c.want, got, c.retryable)
			}
		})
	}
}

func TestFixNamesAnActionForNonRetryableOnly(t *testing.T) {
	for _, c := range []FailureClass{FailureAuth, FailureCLIVersion, FailureModel} {
		if Fix(c) == "" {
			t.Errorf("Fix(%q) is empty, want a one-line fix", c)
		}
	}
	for _, c := range []FailureClass{FailureLimit, FailureCrash, FailureTransient, FailureUnknown} {
		if Fix(c) != "" {
			t.Errorf("Fix(%q) = %q, want empty for a retryable class", c, Fix(c))
		}
	}
}
