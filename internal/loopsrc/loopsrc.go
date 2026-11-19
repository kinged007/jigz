// Package loopsrc is where Jigz gets the live state of the loops. A
// page shows a control or a field only when the source reports the
// capability for it, so nothing waits on a Jigz release.
package loopsrc

import (
	"context"
	"time"

	"github.com/kinged007/jigz/internal/rollup"
)

// Capabilities says what a source can report or do beyond the basics
// every source has: loops, running and retrying counts, and totals.
type Capabilities struct {
	Sleep      bool `json:"sleep"`
	Wake       bool `json:"wake"`
	Budget     bool `json:"budget"`
	Pause      bool `json:"pause"`
	Cleanup    bool `json:"cleanup"`
	Logs       bool `json:"logs"`
	ConfigHash bool `json:"config_hash"`
}

// Source reads the fleet. Starting and stopping stay with
// internal/supervise, which signals the Jigz child.
type Source interface {
	Name() string
	Capabilities() Capabilities
	Fleet() rollup.Fleet
}

// Legacy reads the shared Jigz registry and each loop's database, which
// is all a Jigz without a status document offers.
type Legacy struct {
	Reader rollup.Reader
}

func (Legacy) Name() string               { return "legacy" }
func (Legacy) Capabilities() Capabilities { return Capabilities{} }
func (l Legacy) Fleet() rollup.Fleet      { return l.Reader.Collect() }

// Operator is a source that reads status documents and drives the
// Jigz commands. Pages use it through a type assertion, and show
// a control only where ProjectCapabilities says the project supports it.
type Operator interface {
	Source
	ProjectCapabilities(project string) Capabilities
	Status(project string) (StatusInfo, error)
	Wake(ctx context.Context, project string) (WakeResult, error)
	Cleanup(ctx context.Context, project string, dryRun bool) (CleanupResult, error)
	LogTail(project, workflow string, lines int) ([]string, error)
	WaitRecycled(ctx context.Context, project, workflow string, before StatusInfo, timeout time.Duration) (StatusInfo, error)
}

var _ Operator = StatusDoc{}
