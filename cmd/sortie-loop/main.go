package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kinged007/sortie-loop/internal/config"
)

// cmdline is the parsed command line.
type cmdline struct {
	setup       bool
	repo        string
	dir         string
	noServer    bool
	noDashboard bool
	unite       bool
	dashPort    int
	dumpVersion bool
	showVersion bool
	// remote is "stop" or "restart" when one of those subcommands was
	// given, and remoteArgs is everything after it.
	remote     string
	remoteArgs []string
}

// errHelp is returned by parseArgs when usage was asked for.
var errHelp = errors.New("help")

// parseArgs reads the command line. A flag that takes a value accepts it
// as the next argument or after "=", and the value is consumed either
// way, so a repository slug is never mistaken for the directory. The
// directory is the last bare argument and may sit anywhere, so
// `sortie-loop setup ./repo --repo owner/name` and
// `sortie-loop setup --repo owner/name ./repo` both target ./repo.
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
		case "stop", "restart":
			// A subcommand: everything after it belongs to runRemote,
			// which does its own parsing, so stop collecting here.
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
		case "--no-server":
			c.noServer = true
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
	usageWriter(`Usage: sortie-loop [--no-server] [--no-dashboard] [--dashboard-port=N] [--unite] [repo-root]
         sortie-loop setup [repo-root] [--repo=owner/name]
         sortie-loop stop [--when-idle] [repo-root]
         sortie-loop restart [--when-idle] [repo-root]
  Run every WORKFLOW.*.md loop in .sortie/workflows/ against the repo at repo-root (default: cwd).
  SORTIE_LOOP_ONLY=plan,build  start only these loops (default: all of them)
  Settings live in <root>/.sortie/config.yaml; repo id defaults to the git remote.
  stop/restart act on the run already serving this repo-root, found through the shared registry.
  stop and restart are immediate; --when-idle waits for each loop to finish what it is working on, one loop at a time.
  --version prints the tool version and the engine it drives; --dump-version prints only the engine version.`)
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
		fmt.Fprintln(os.Stderr, "sortie-loop:", fe.err)
		code = 1
	}()
	c, err := parseArgs(os.Args[1:])
	if errors.Is(err, errHelp) {
		usage()
		return 0
	}
	if err != nil {
		fatal(err)
	}
	if c.dumpVersion {
		fmt.Println(engineVersion())
		return 0
	}
	if c.showVersion {
		fmt.Printf("sortie-loop %s (engine %s)\n", version, engineVersion())
		return 0
	}
	if c.setup {
		runSetup(c.dir, c.repo)
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
	noServer, noDashboard, dashPort, unite := c.noServer, c.noDashboard, c.dashPort, c.unite
	abs, err := filepath.Abs(dir)
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
	bin, err := resolveSortieBin()
	if err != nil {
		fatal(err)
	}
	// SORTIE_LOOP_ONLY selects a subset of the installed loops, for a
	// supervisor driving several repos. Unset starts every loop, so a
	// plain sortie-loop run is unchanged.
	loops, err := selectLoops(discoverWorkflows(abs), os.Getenv("SORTIE_LOOP_ONLY"))
	if err != nil {
		fatal(err)
	}
	if len(loops) == 0 {
		fatal(fmt.Errorf("no WORKFLOW.*.md files in %s", filepath.Join(abs, ".sortie", "workflows")))
	}
	for _, l := range loops {
		fmt.Printf("loop %-10s %s filter %q\n", l.name, l.file, cfg.FilterFor(l.name))
	}
	var lock *portLock
	if !noServer {
		// Serialize the port claim and child startup across sortie-loop
		// processes: two runs started together else probe before either
		// has bound, pick the same ports, and the later child loses the
		// bind (sortie exits when its HTTP server cannot bind).
		lock, err = lockStarts()
		if err != nil {
			fatal(fmt.Errorf("port lock: %w", err))
		}
	}
	// ponytail: one shared HTTP port per loop would collide; probe
	// upward from 7678 for a free port per loop (--no-server passes
	// --port 0 to disable loop servers entirely).
	loopPorts := make([]int, len(loops))
	if noServer {
		for i := range loopPorts {
			loopPorts[i] = 0
		}
	} else {
		var dash int
		// An explicit --dashboard-port=N always starts a dashboard on N
		// (fatal if busy). Otherwise a --unite run joins the live unite
		// dashboard when one answers, instead of starting a second one.
		if unite && dashPort == 0 && !noDashboard {
			if live := findUniteDashboard(); live > 0 {
				dashPort = -live
			}
		}
		loopPorts, dash, err = claimPorts(len(loops), dashPort, noDashboard)
		if err != nil {
			fatal(err)
		}
		if dashPort >= 0 {
			dashPort = dash
		}
	}
	names := make([]string, len(loops))
	for i, l := range loops {
		names[i] = l.name
	}
	sup := newSupervisor(abs, bin, loops, loopPorts)
	// Ordered rather than deferred in the body: os.Exit would skip a
	// defer, and the children and the registry entry both have to be
	// cleaned up on the way out whatever ends the run — including a
	// fatal from a loop that failed to start after others were running.
	defer func() {
		sup.shutdown()
		sup.closeStatus()
		dropRegistry(abs)
		if lock != nil {
			lock.release()
		}
	}()
	if err := sup.startAll(names); err != nil {
		fatal(err)
	}
	sup.republish()
	// Startup done: every child bound its port, so the next run's probe
	// sees them; free the port-claim lock for the rest of the lifetime.
	if lock != nil {
		lock.release()
	}
	// dashPort is negative when this run joins a live unite dashboard:
	// its loops register above, and the existing page picks them up.
	if !noDashboard && dashPort > 0 {
		serveDashboard(dashPort, unite, sup)
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
