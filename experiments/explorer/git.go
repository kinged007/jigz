package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Commit is one first-parent commit of the default branch.
type Commit struct {
	Hash    string       `json:"hash"`
	Author  string       `json:"author"`
	Email   string       `json:"email"`
	At      int64        `json:"at"`
	Subject string       `json:"subject"`
	Files   []FileChange `json:"files"`
}

type FileChange struct {
	Path string `json:"path"`
	Add  int    `json:"add"`
	Del  int    `json:"del"`
}

// gitEnv passes the token as an HTTP header through git's environment
// config, so it never appears in argv or in the clone's config file.
func gitEnv(token string) []string {
	h := "AUTHORIZATION: basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	return append(os.Environ(), "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0="+h,
		"GIT_TERMINAL_PROMPT=0")
}

// syncGit keeps a bare clone of the repository in the data directory. The
// commit history comes from git, which costs no API rate limit.
func syncGit(ctx context.Context, dataDir, owner, name, branch, token string) error {
	dir := filepath.Join(dataDir, "repo.git")
	url := fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
	var cmd *exec.Cmd
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		cmd = exec.CommandContext(ctx, "git", "clone", "--bare", "--quiet", url, dir)
	} else {
		cmd = exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--quiet", "--prune", url,
			fmt.Sprintf("+refs/heads/%s:refs/heads/%s", branch, branch))
	}
	cmd.Env = gitEnv(token)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git: %w", err)
	}
	return nil
}

const commitMark = "\x1e"

func readCommits(dataDir, branch string) ([]Commit, error) {
	dir := filepath.Join(dataDir, "repo.git")
	out, err := exec.Command("git", "-C", dir, "log", branch, "--first-parent", "-m", "--numstat",
		"--format="+commitMark+"%H%x1f%an%x1f%ae%x1f%at%x1f%s").Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return parseGitLog(out), nil
}

func parseGitLog(out []byte) []Commit {
	var commits []Commit
	for _, chunk := range bytes.Split(out, []byte(commitMark)) {
		if len(bytes.TrimSpace(chunk)) == 0 {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(chunk))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		if !sc.Scan() {
			continue
		}
		f := strings.Split(sc.Text(), "\x1f")
		if len(f) < 5 {
			continue
		}
		at, _ := strconv.ParseInt(f[3], 10, 64)
		c := Commit{Hash: f[0], Author: f[1], Email: f[2], At: at, Subject: f[4]}
		for sc.Scan() {
			parts := strings.SplitN(sc.Text(), "\t", 3)
			if len(parts) != 3 {
				continue
			}
			add, _ := strconv.Atoi(parts[0]) // "-" for binary files
			del, _ := strconv.Atoi(parts[1])
			c.Files = append(c.Files, FileChange{Path: renamedPath(parts[2]), Add: add, Del: del})
		}
		commits = append(commits, c)
	}
	return commits
}

// renamedPath resolves numstat rename notation ("a/{x => y}/b", "x => y") to the new path.
func renamedPath(p string) string {
	if i := strings.Index(p, "{"); i >= 0 {
		if j := strings.Index(p[i:], "}"); j >= 0 {
			inner := p[i+1 : i+j]
			if _, after, ok := strings.Cut(inner, " => "); ok {
				return strings.ReplaceAll(p[:i]+after+p[i+j+1:], "//", "/")
			}
		}
	}
	if _, after, ok := strings.Cut(p, " => "); ok {
		return after
	}
	return p
}

func listFiles(dataDir, branch string) (map[string]bool, error) {
	out, err := exec.Command("git", "-C", filepath.Join(dataDir, "repo.git"), "ls-tree", "-r", "--name-only", branch).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree: %w", err)
	}
	files := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" {
			files[l] = true
		}
	}
	return files, nil
}
