package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kinged007/jigz/internal/config"
	"github.com/kinged007/jigz/internal/engine"
)

// cmdline is the parsed command line.
type cmdline struct {
	setup       bool
	setupJSON   bool
	repo        string
	dir         string
	noDashboard bool
	unite       bool
	dashPort    int
	host        string
	dumpVersion bool
	showVersion bool
	// remote is "stop", "restart", "status", "cleanup", "wake", "pause", "resume" or "budget" when one of those
	// subcommands was given, and remoteArgs is everything after it.
	remote     string
	remoteArgs []string
}

// errHelp is returned by parseArgs when usage was asked for.
var errHelp = errors.New("help")

// parseArgs reads the command line. A flag that takes a value accepts it
// as the next argument or after "=", and the value is consumed either
// way, so a repository slug is never mistaken for the directory. The
// directory is the last bare argument and may sit anywhere, so
// `jig setup ./repo --repo owner/name` and
// `jig setup --repo owner/name ./repo` both target ./repo.
func parseArgs(argv []string) (cmdline, error) {
	var c cmdline
	var bare []string
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		name, inline, hasInline := strings.Cut(arg, "=")
		value := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			if i+1 >= len(argv) {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return argv[i], nil
		}
		switch name {
		case "setup":
			c.setup = true
		case "--json":
			if c.setup {
				c.setupJSON = true
				continue
			}
			return c, fmt.Errorf("unknown flag %q", arg)
		case "stop", "restart", "status", "cleanup", "wake", "pause", "resume", "budget", "reload", "validate", "agent-exec":
			// A subcommand: everything after it belongs to runRemote,
			// which does its own parsing, so stop collecting here.
			c.remote = name
			c.remoteArgs = argv[i+1:]
			i = len(argv)
		case "start", "join", "leave":
			c.remote = name
			c.remoteArgs = argv[i+1:]
			i = len(argv)
		case "--dump-version":
			c.dumpVersion = true
		case "--version", "-V":
			c.showVersion = true
		case "help", "-h", "--help":
			return c, errHelp
		case "--repo":
			v, err := value()
			if err != nil {
				return c, err
			}
			c.repo = v
		case "--no-dashboard":
			c.noDashboard = true
		case "--unite":
			c.unite = true
		case "--dashboard-port":
			v, err := value()
			if err != nil {
				return c, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return c, fmt.Errorf("invalid --dashboard-port=%q", v)
			}
			c.dashPort = n
		case "--host":
			v, err := value()
			if err != nil {
				return c, err
			}
			c.host = v
		default:
			if strings.HasPrefix(arg, "-") {
				return c, fmt.Errorf("unknown flag %q", arg)
			}
			bare = append(bare, arg)
		}
	}
	if len(bare) > 0 {
		c.dir = bare[len(bare)-1]
	}
	return c, nil
}

var usageWriter = func(s string) { fmt.Println(s) }

func usage() {
	usageWriter(`jig runs the workflow loops of a repo and manages the run.

Usage
  jig [options] [repo-root]       run in the foreground (Ctrl-C stops)
  jig <command> [args]

Run
  start [options] [repo-root]     run in the background and print its pid,
                                  dashboard address and log file; does
                                  nothing when a run already serves the root
  [repo-root]                     defaults to the current directory; loops
                                  are the WORKFLOW.*.md files in
                                  .jigz/workflows/, and JIG_ONLY=plan,build
                                  starts only those

Control (act on the run serving repo-root, found through the registry)
  stop [--when-idle]              stop now, or let each loop finish its
                                  current item first, one loop at a time;
                                  then list the other runs still going
  restart [--when-idle]           restart, immediately or when idle
  status [--json]                 show the state of the run and list the
                                  other runs; --json adds liveness and
                                  instances fields
  wake [--json]                   start the loops of a sleeping run
  pause <workflow> [--json]       stop one loop and keep it stopped, also
                                  after a budget pause
  resume <workflow> [--json]      start it again; at or over its cap this
                                  also turns the cap off for the period
  budget override <workflow>      lift a budget pause and turn the cap off
                                  for the rest of the period
  reload [--json]                 re-read .jigz/config.yaml without
                                  restarting loops; sleep, cleanup and
                                  budgets apply now, workflow filter
                                  changes need restart --when-idle
  cleanup [--dry-run] [--json]    run the configured cleanup command once
                                  and exit non-zero when it fails
  validate [--json]               check config.yaml and run the engine's
                                  validation on each workflow

Setup
  setup [--repo=owner/name] [--json]
                                  write config, labels and workflow files
                                  in repo-root; the repo id defaults to the
                                  git remote
  join <hub-url> <token> [--name <name>] [--no-service] [--yes]
                                  enroll this machine with a hub and
                                  install the jigd service; token - reads
                                  stdin; the tracker token comes from
                                  JIG_TOKEN, GITHUB_TOKEN, GH_TOKEN or
                                  gh auth token
  leave                           remove the service and hub.json; loop
                                  roots are kept
  daemon                          run the hub daemon in the foreground
                                  (the jigd service runs it in the
                                  background; a binary named jigd does the
                                  same)

Options for the run and for start
  --host=ADDR                     dashboard bind address, default 127.0.0.1;
                                  0.0.0.0 listens on all interfaces. The
                                  dashboard has no authentication.
  --dashboard-port=N              dashboard port
  --no-dashboard                  do not serve the dashboard
  --unite                         join or start the shared dashboard

Other
  --version, -V                   print the tool and engine versions
  --dump-version                  print only the engine version
  --help, -h                      print this help

Settings are in <repo-root>/.jigz/config.yaml.`)
}

// isJigdName reports whether the executable was invoked as jigd, the
// name the installed service runs.
func isJigdName(arg0 string) bool {
	base := filepath.Base(arg0)
	return base == "jigd" || base == "jigd.exe"
}

func main() { os.Exit(run()) }

func run() (code int) {
	// fatal reports through a panic so the deferred cleanup below runs on
	// every error path, including a loop that failed to start after
	// earlier loops were already running.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		fe, ok := r.(fatalError)
		if !ok {
			panic(r)
		}
		fmt.Fprintln(os.Stderr, "jig:", fe.err)
		code = 1
	}()
	if isJigdName(os.Args[0]) {
		return runDaemon(os.Args[1:])
	}
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		return runDaemon(os.Args[2:])
	}
	c, err := parseArgs(os.Args[1:])
	if errors.Is(err, errHelp) {
		usage()
		return 0
	}
	if err != nil {
		fatal(fmt.Errorf("%w (jig --help lists the commands)", err))
	}
	if c.dumpVersion || c.showVersion {
		spec, err := engine.Configured()
		if err != nil {
			fatal(err)
		}
		if c.dumpVersion {
			fmt.Println(spec.Version())
		} else {
			fmt.Printf("Jigz %s (engine %s)\n", version, spec.Tag)
		}
		return 0
	}
	if c.setup {
		runSetup(c.dir, c.repo, c.setupJSON)
		return 0
	}
	if c.remote == "start" {
		return runStart(c.remoteArgs)
	}
	if c.remote == "join" {
		runJoin(c.remoteArgs)
		return 0
	}
	if c.remote == "leave" {
		runLeave(c.remoteArgs)
		return 0
	}
	if c.remote == "status" {
		runStatus(c.remoteArgs)
		return 0
	}
	if c.remote == "wake" {
		runWake(c.remoteArgs)
		return 0
	}
	if c.remote == "pause" || c.remote == "resume" || c.remote == "budget" {
		runBudgetCmd(c.remote, c.remoteArgs)
		return 0
	}
	if c.remote == "cleanup" {
		runCleanupCmd(c.remoteArgs)
		return 0
	}
	if c.remote == "reload" {
		runReload(c.remoteArgs)
		return 0
	}
	if c.remote == "agent-exec" {
		return runAgentExec(c.remoteArgs, os.Stdin, os.Stdout, os.Stderr)
	}
	if c.remote == "validate" {
		runValidate(c.remoteArgs)
		return 0
	}
	if c.remote != "" {
		runRemote(c.remote, c.remoteArgs)
		return 0
	}
	dir := c.dir
	if dir == "" {
		dir = "."
	}
	noDashboard, dashPort, unite, hostFlag := c.noDashboard, c.dashPort, c.unite, c.host
	abs, err := absRoot(dir)
	if err != nil {
		fatal(err)
	}
	cfg, err := config.Load(abs)
	if err != nil {
		fatal(err)
	}
	// Checked before anything is started: an unauthenticated run would
	// otherwise claim an issue and start an agent before the engine
	// reported the failure.
	if err := cfg.CheckToken(); err != nil {
		fatal(err)
	}
	if err := cfg.CheckCleanup(); err != nil {
		fatal(err)
	}
	if err := cfg.CheckBudgets(); err != nil {
		fatal(err)
	}
	for name, b := range cfg.Budgets {
		if b.MaxUSD > 0 {
			fmt.Fprintf(os.Stderr, "jig: workflows.%s.budget.max_usd is ignored: the engine records no cost per run\n", name)
		}
	}
	bin, err := resolveSortieBin()
	if err != nil {
		fatal(err)
	}
	// JIG_ONLY selects a subset of the installed loops, for a
	// supervisor driving several repos. Unset starts every loop, so a
	// plain Jigz run is unchanged.
	srcLoops, err := filesSource(abs, cfg.Repo, os.Getenv("JIG_ONLY")).Loops(context.Background())
	if err != nil {
		fatal(err)
	}
	loops := loopDefs(srcLoops)
	if len(loops) == 0 {
		fatal(fmt.Errorf("no WORKFLOW.*.md files in %s", filepath.Join(abs, ".jigz", "workflows")))
	}
	for _, l := range loops {
		fmt.Printf("loop %-10s %s filter %q\n", l.name, l.file, cfg.FilterFor(l.name))
	}
	// An explicit --dashboard-port=N always starts a dashboard on N (fatal
	// if busy). Otherwise a --unite run joins the live unite dashboard
	// when one answers, instead of starting a second one.
	if unite && dashPort == 0 && !noDashboard {
		if live := findUniteDashboard(); live > 0 {
			dashPort = -live
		}
	}
	dashPort, err = claimDashboard(dashPort, noDashboard)
	if err != nil {
		fatal(err)
	}
	names := make([]string, len(loops))
	for i, l := range loops {
		names[i] = l.name
	}
	sup := newSupervisor(abs, bin, loops)
	switch {
	case noDashboard:
	case dashPort > 0:
		sup.dash = dashInfo{URL: dashboardURL(hostFlag, dashPort), Serve: true}
	case dashPort < 0:
		sup.dash = dashInfo{URL: fmt.Sprintf("http://127.0.0.1:%d", -dashPort)}
	}
	sup.setGate(engine.Detect(bin))
	sup.sleepCfg = cfg.Sleep
	sup.initBudget(cfg.Budgets)
	// Ordered rather than deferred in the body: os.Exit would skip a
	// defer, and the children and the registry entry both have to be
	// cleaned up on the way out whatever ends the run — including a
	// fatal from a loop that failed to start after others were running.
	defer func() {
		sup.shutdown()
		sup.closeStatus()
		dropRegistry(abs)
	}()
	if err := sup.startAll(names); err != nil {
		fatal(err)
	}
	sup.republish()
	if cfg.Cleanup.Enabled {
		sup.startCleanup(cfg)
	}
	// dashPort is negative when this run joins a live unite dashboard:
	// its loops register above, and the existing page picks them up.
	if !noDashboard && dashPort > 0 {
		serveDashboard(dashPort, unite, sup, hostFlag)
	} else if dashPort < 0 {
		fmt.Printf("dashboard http://127.0.0.1:%d (unite: all repos)\n", -dashPort)
	}
	return sup.run()
}

// fatalError carries a message out to run's recover, so the deferred
// cleanup still runs before the process exits. Calling os.Exit from here
// would skip it and leave loop children running.
type fatalError struct{ err error }

func fatal(err error) { panic(fatalError{err}) }
