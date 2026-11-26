package machine

import "regexp"

// FailureClass names why one attempt failed. The first three are
// non-retryable: no retry fixes them, so the item is parked with a
// notice. The rest keep today's behaviour.
type FailureClass string

const (
	// FailureAuth is an unauthenticated agent: a missing, wrong or
	// expired credential.
	FailureAuth FailureClass = "agent-unauthenticated"
	// FailureCLIVersion is an agent CLI that cannot run: it is missing
	// or its version is refused.
	FailureCLIVersion FailureClass = "agent-cli-unusable"
	// FailureModel is a model that is unknown or not configured.
	FailureModel FailureClass = "missing-model"
	// FailureLimit is a usage limit with a reset time.
	FailureLimit FailureClass = "usage-limit"
	// FailureCrash is a crashed engine or agent process.
	FailureCrash FailureClass = "engine-crash"
	// FailureTransient is a temporary network or server failure.
	FailureTransient FailureClass = "transient-network"
	// FailureUnknown matches nothing and keeps today's retry behaviour.
	FailureUnknown FailureClass = "unknown"
)

// Retryable reports whether the class keeps today's retry path. Only the
// three non-retryable classes park the item.
func Retryable(c FailureClass) bool {
	switch c {
	case FailureAuth, FailureCLIVersion, FailureModel:
		return false
	}
	return true
}

// Fix is the one-line fix recorded with a non-retryable notice, empty
// for retryable classes.
func Fix(c FailureClass) string {
	switch c {
	case FailureAuth:
		return "log the agent back in (run /login)"
	case FailureCLIVersion:
		return "upgrade the agent CLI to a supported version"
	case FailureModel:
		return "configure a valid model"
	}
	return ""
}

// BuiltinCLIVersion matches the Jigz-side wording of an agent CLI that
// cannot run: the version check refusing a release, or the command
// itself missing. It reuses no engine text on purpose: the version
// rules live in VersionProblem and the missing-command text in
// agent-exec. A bare "executable file not found" without the agent
// command prefix is a generic Go exec error and must not park.
var BuiltinCLIVersion = regexp.MustCompile(`(?i)agent command (?:not found|is empty)|no version that can be read|is not supported; install`)

// missingModelRe is the model-availability subset of BuiltinUnusable:
// an agent with no model to run, as opposed to a bad credential.
var missingModelRe = regexp.MustCompile(`(?i)no models? (?:selected|available|configured)|no default model|model (?:is )?(?:not configured|required)`)

// BuiltinCrash matches a crashed process. It stays narrow on purpose:
// an agent tool error mentioning a crash word must not park an item.
var BuiltinCrash = regexp.MustCompile(`(?i)\bpanic:|\bfatal error\b|segmentation fault|\bsegfault\b|out of memory|stack overflow|\bruntime error:`)

// BuiltinTransient matches a temporary network or server failure. A
// usage-limit message mentioning a retry never reaches it: the limit
// check runs first.
var BuiltinTransient = regexp.MustCompile(`(?i)timed? ?out|deadline exceeded|temporar\w+ (?:unavailable|failure|error)|connection (?:reset|refused|aborted|timed out)|network (?:unreachable|error|timed? ?out)|econnreset|econnrefused|etimedout|eai_again|socket hang up|\b50[234]\b|bad gateway|service unavailable`)

// Classify names why one attempt failed from its stored error text.
// Precedence is fixed: usage limit first (as in IsUnusable), then
// missing model, CLI or version, authentication, crash, transient.
// Anything matching nothing is unknown and keeps today's retry
// behaviour without ever parking silently.
func Classify(text string) FailureClass {
	switch {
	case BuiltinLimit.MatchString(text):
		return FailureLimit
	case BuiltinModelError.MatchString(text) || missingModelRe.MatchString(text):
		return FailureModel
	case BuiltinCLIVersion.MatchString(text):
		return FailureCLIVersion
	case BuiltinUnusable.MatchString(text):
		return FailureAuth
	case BuiltinCrash.MatchString(text):
		return FailureCrash
	case BuiltinTransient.MatchString(text):
		return FailureTransient
	}
	return FailureUnknown
}
