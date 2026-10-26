package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/collector"
	"github.com/kinged007/jigz/internal/config"
	fleetconfig "github.com/kinged007/jigz/internal/fleetconfig"
	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/supervise"
)

// questionsCheck is how often questionsLoop looks whether a pass is due.
const questionsCheck = 15 * time.Second

func (d *daemon) newScanner() *collector.Scanner {
	return &collector.Scanner{
		Store:      d.store,
		Settings:   d.questionSettings,
		Targets:    d.questionTargets,
		Questions:  d.questionsOn,
		Qualifier:  "label:" + d.opt.hub.RunnerLabel,
		SearchOnly: true,
		OnResume:   d.questionResumed,
	}
}

// questionsLoop runs the scanner once loops exist. It also runs while the
// daemon is paused, because a resume only changes labels. It stops when
// the credential is refused.
func (d *daemon) questionsLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if !d.isRevoked() && len(d.questionTargets()) > 0 {
			d.scan.Once(ctx)
		}
		_ = d.opt.sleep(ctx, questionsCheck)
	}
}

// configuredLoops are the loops that are still in the hub config.
func (d *daemon) configuredLoops() []supervise.Child {
	var out []supervise.Child
	for _, c := range d.sup.Snapshot() {
		d.mu.Lock()
		gone := d.retired[c.Key]
		d.mu.Unlock()
		if !gone {
			out = append(out, c)
		}
	}
	return out
}

func (d *daemon) questionTargets() []collector.Target {
	var out []collector.Target
	seen := map[string]bool{}
	for _, c := range d.configuredLoops() {
		p := strings.ToLower(c.Loop.Project)
		if !seen[p] {
			seen[p] = true
			out = append(out, collector.Target{Project: c.Loop.Project, Token: d.opt.hub.TrackerToken})
		}
	}
	return out
}

// questionsOn reads github.questions from the first loop root of the
// project. A root whose config cannot be read is not scanned.
func (d *daemon) questionsOn(project string) bool {
	for _, c := range d.configuredLoops() {
		if strings.EqualFold(c.Loop.Project, project) {
			g, err := config.LoadGitHub(c.Loop.Root)
			return err == nil && g.QuestionsOn()
		}
	}
	return false
}

func (d *daemon) questionSettings() fleetconfig.GitHub {
	endpoint := ""
	for _, c := range d.configuredLoops() {
		if m := readLoopMeta(c.Loop); m.kind == hubproto.KindIssue && m.endpoint != "" {
			endpoint = m.endpoint
			break
		}
	}
	return fleetconfig.GitHub{APIURL: d.apiBase(endpoint)}
}

// questionResumed logs a resume and wakes the loops whose filter matches
// the item's labels now. A paused daemon wakes nothing.
func (d *daemon) questionResumed(project string, number int, trigger string) {
	d.log.Printf("questions: resumed %s#%d with %s", project, number, trigger)
	it, err := d.store.QuestionItem(project, number)
	if err != nil || d.isPaused() || d.isRevoked() {
		return
	}
	for _, c := range d.configuredLoops() {
		if !strings.EqualFold(c.Loop.Project, project) || !readLoopMeta(c.Loop).fitsLabels(it.IsPR, it.Labels) {
			continue
		}
		if err := d.wake(c.Key); err != nil {
			d.setErr(fmt.Errorf("%s: %w", c.Key, err))
		}
	}
}
