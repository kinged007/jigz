package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kinged007/jigz/internal/dirs"
	"github.com/kinged007/jigz/internal/engine"
	"github.com/kinged007/jigz/internal/github"
	"github.com/kinged007/jigz/internal/host"
	"github.com/kinged007/jigz/internal/hubclient"
	"github.com/kinged007/jigz/internal/hubproto"
	"github.com/kinged007/jigz/internal/hubstate"
	"github.com/kinged007/jigz/internal/service"
)

const maxEnrollName = 40

type joinOpts struct {
	hubURL    string
	token     string
	name      string
	noService bool
	yes       bool
}

// joinDeps are the effects of join and leave, replaced in tests.
type joinDeps struct {
	stdin     io.Reader
	out       io.Writer
	getenv    func(string) string
	ghToken   func() (string, error)
	githubURL string
	service   *service.Manager
	exe       func() (string, error)
	hostname  func() (string, error)
	logPath   func() (string, error)
}

func parseJoinArgs(args []string) (joinOpts, error) {
	var o joinOpts
	var bare []string
	for i := 0; i < len(args); i++ {
		name, inline, hasInline := strings.Cut(args[i], "=")
		switch {
		case name == "--no-service" && !hasInline:
			o.noService = true
		case name == "--yes" && !hasInline, name == "-y" && !hasInline:
			o.yes = true
		case name == "--name":
			if hasInline {
				o.name = inline
			} else if i+1 < len(args) {
				i++
				o.name = args[i]
			} else {
				return o, errors.New("--name needs a value")
			}
		case args[i] != "-" && strings.HasPrefix(args[i], "-"):
			return o, fmt.Errorf("unknown flag %q", name)
		default:
			bare = append(bare, args[i])
		}
	}
	if len(bare) != 2 {
		return o, errors.New("usage: jig join <hub-url> <token> [--name <name>] [--no-service] [--yes]")
	}
	o.hubURL, o.token = bare[0], bare[1]
	return o, nil
}

// checkHubURL accepts https, and http on a loopback host. It returns the URL
// without a trailing slash. Errors never repeat the URL's user info.
func checkHubURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("hub URL is not valid: use https://host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("hub URL must not contain user info, a query or a fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return "", fmt.Errorf("hub URL must use https (http is allowed only for localhost): %s://%s", u.Scheme, u.Host)
		}
	default:
		return "", errors.New("hub URL must start with https://")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func readJoinToken(token string, stdin io.Reader) (string, error) {
	if token == "-" {
		data, err := io.ReadAll(io.LimitReader(stdin, 4096))
		if err != nil {
			return "", fmt.Errorf("reading the token from stdin: %w", err)
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("the enrollment token is empty")
	}
	return token, nil
}

func trackerToken(d joinDeps) (string, error) {
	for _, name := range []string{"JIG_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(d.getenv(name)); v != "" {
			return v, nil
		}
	}
	if d.ghToken != nil {
		if v, err := d.ghToken(); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("no tracker token: set JIG_TOKEN, GITHUB_TOKEN or GH_TOKEN, or run gh auth login")
}

func redactAll(s string, secrets ...string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

func (d joinDeps) confirm(prompt string, r **bufio.Reader) bool {
	fmt.Fprint(d.out, prompt+" [y/N] ")
	if *r == nil {
		*r = bufio.NewReader(d.stdin)
	}
	line, _ := (*r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func joinRun(ctx context.Context, d joinDeps, args []string) (err error) {
	o, err := parseJoinArgs(args)
	if err != nil {
		return err
	}
	hubURL, err := checkHubURL(o.hubURL)
	if err != nil {
		return err
	}
	enrollToken, err := readJoinToken(o.token, d.stdin)
	if err != nil {
		return err
	}
	tracker, err := trackerToken(d)
	if err != nil {
		return err
	}
	secrets := []string{enrollToken, tracker}
	defer func() {
		if err != nil {
			err = errors.New(redactAll(err.Error(), secrets...))
		}
	}()

	var reader *bufio.Reader
	if !o.yes && (hubstate.Exists() || (!o.noService && d.service != nil && d.service.Installed())) {
		if !d.confirm("This machine is already enrolled. Replace hub.json and the service?", &reader) {
			return errors.New("join cancelled: the existing enrollment is unchanged")
		}
	}
	if !o.noService {
		if d.service == nil {
			return errors.New("the service is not supported here: use --no-service")
		}
		if err := d.service.Preflight(); err != nil {
			if errors.Is(err, service.ErrNoSession) {
				return errors.New("no systemd user session is available: use --no-service and run `jig daemon` yourself")
			}
			return err
		}
	}

	gh := &github.Client{BaseURL: d.githubURL}
	login, err := gh.User(ctx, "join", tracker)
	if err != nil {
		return fmt.Errorf("checking the tracker token: %w", err)
	}

	name := o.name
	if name == "" {
		if name, err = d.hostname(); err != nil || name == "" {
			name = "jig"
		}
		if len(name) > maxEnrollName {
			name = name[:maxEnrollName]
		}
	}
	spec, err := engine.Configured()
	if err != nil {
		return err
	}
	req := hubproto.EnrollRequest{
		Token: enrollToken,
		Name:  name,
		Host: hubproto.Host{
			OS:          runtime.GOOS,
			Arch:        runtime.GOARCH,
			CPUs:        runtime.NumCPU(),
			MemoryBytes: host.New("").Read().MemTotal,
		},
		JigVersion:    version,
		EngineVersion: spec.Version(),
		TrackerLogin:  login,
	}
	client := &hubclient.Client{URL: hubURL, Version: version, Secrets: secrets}
	resp, err := client.Enroll(ctx, req)
	if err != nil {
		return fmt.Errorf("enrolling: %w", err)
	}
	secrets = append(secrets, resp.Credential)
	if err := hubstate.Save(hubstate.State{
		HubURL:       hubURL,
		DaemonID:     resp.DaemonID,
		Name:         name,
		Credential:   resp.Credential,
		RunnerLabel:  resp.RunnerLabel,
		TrackerToken: tracker,
	}); err != nil {
		return fmt.Errorf("writing hub.json: %w", err)
	}
	fmt.Fprintf(d.out, "Enrolled %q as %s (tracker login %s).\n", name, resp.RunnerLabel, login)
	if o.noService {
		fmt.Fprintln(d.out, "No service installed. Run `jig daemon` to start the daemon.")
		return nil
	}
	exe, err := d.exe()
	if err != nil {
		return fmt.Errorf("enrolled, but the service was not installed: %w", err)
	}
	logPath, err := d.logPath()
	if err != nil {
		return fmt.Errorf("enrolled, but the service was not installed: %w", err)
	}
	res, err := d.service.Install(service.Spec{Exe: exe, Path: d.getenv("PATH"), LogPath: logPath})
	if err != nil {
		return fmt.Errorf("enrolled, but the service was not installed: %w; run `jig daemon` instead", err)
	}
	path, _ := d.service.FilePath()
	fmt.Fprintf(d.out, "Service installed and started: %s\n", path)
	if res.LingerHint != "" {
		fmt.Fprintf(d.out, "The service stops when you log out. To keep it running, run:\n  %s\n", res.LingerHint)
	}
	return nil
}

func leaveRun(d joinDeps) error {
	enrolled := hubstate.Exists()
	installed := d.service != nil && d.service.Installed()
	if !enrolled && !installed {
		fmt.Fprintln(d.out, "This machine is not enrolled.")
		return nil
	}
	if installed {
		if err := d.service.Remove(); err != nil {
			return fmt.Errorf("removing the service: %w", err)
		}
		fmt.Fprintln(d.out, "Service removed.")
	}
	if err := hubstate.Remove(); err != nil {
		return err
	}
	fmt.Fprintln(d.out, "Left the hub: hub.json removed. Loop roots are kept. A daemon started by hand keeps running until you stop it.")
	return nil
}

func realJoinDeps() joinDeps {
	api := os.Getenv("JIG_GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	d := joinDeps{
		stdin:     os.Stdin,
		out:       os.Stdout,
		getenv:    os.Getenv,
		githubURL: strings.TrimRight(api, "/"),
		hostname:  os.Hostname,
		ghToken: func() (string, error) {
			out, err := exec.Command("gh", "auth", "token").Output()
			return string(out), err
		},
		exe: func() (string, error) {
			p, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(p)
		},
		logPath: func() (string, error) {
			dir, err := dirs.State()
			return filepath.Join(dir, "jigd.log"), err
		},
	}
	if home, err := os.UserHomeDir(); err == nil && (runtime.GOOS == "linux" || runtime.GOOS == "darwin") {
		username := os.Getenv("USER")
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
		d.service = &service.Manager{
			GOOS: runtime.GOOS,
			Home: home,
			UID:  os.Getuid(),
			User: username,
			Run: func(name string, args ...string) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
				return string(out), err
			},
		}
	}
	return d
}

func runJoin(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := joinRun(ctx, realJoinDeps(), args); err != nil {
		fatal(err)
	}
}

func runLeave(args []string) {
	if len(args) > 0 {
		fatal(errors.New("usage: jig leave"))
	}
	if err := leaveRun(realJoinDeps()); err != nil {
		fatal(err)
	}
}
