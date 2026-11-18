package updater

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Commit struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

type Branch struct {
	Name   string `json:"name"`
	Commit Commit `json:"commit"`
	// Ahead / Behind count commits relative to the running version; -1 when
	// that is unknown (first install, or a commit no longer in the repo).
	Ahead  int  `json:"ahead"`
	Behind int  `json:"behind"`
	Active bool `json:"active"`
}

// refPattern accepts branch names and commit hashes and nothing that git could
// read as an option.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

func validRef(ref string) bool {
	return refPattern.MatchString(ref) && !strings.Contains(ref, "..")
}

// Fetch runs git fetch unless one ran in the last maxAge.
func (u *Updater) Fetch(ctx context.Context, maxAge time.Duration) error {
	u.fetchMu.Lock()
	defer u.fetchMu.Unlock()
	if maxAge > 0 && u.now().Sub(u.fetchedAt) < maxAge {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := u.output(ctx, "git", "fetch", "--prune", "--tags", "--force", u.cfg.Remote); err != nil {
		return err
	}
	u.fetchedAt = u.now()
	return nil
}

const logFormat = "%H%x09%an%x09%cI%x09%s"

func parseCommit(line string) (Commit, bool) {
	f := strings.SplitN(line, "\t", 4)
	if len(f) != 4 {
		return Commit{}, false
	}
	d, _ := time.Parse(time.RFC3339, f[2])
	return Commit{SHA: f[0], Short: short(f[0]), Author: f[1], Date: d, Subject: f[3]}, true
}

func (u *Updater) Branches(ctx context.Context, running string) ([]Branch, error) {
	out, err := u.output(ctx, "git", "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)%09%(objectname)%09%(authorname)%09%(committerdate:iso-strict)%09%(subject)",
		"refs/remotes/"+u.cfg.Remote)
	if err != nil {
		return nil, err
	}
	prefix := u.cfg.Remote + "/"
	var res []Branch
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) != 2 || !strings.HasPrefix(f[0], prefix) {
			continue
		}
		name := strings.TrimPrefix(f[0], prefix)
		if name == "HEAD" || name == "" {
			continue
		}
		c, ok := parseCommit(f[1])
		if !ok {
			continue
		}
		b := Branch{Name: name, Commit: c, Ahead: -1, Behind: -1, Active: running != "" && c.SHA == running}
		if running != "" {
			b.Ahead, b.Behind = u.aheadBehind(ctx, running, c.SHA)
		}
		res = append(res, b)
	}
	return res, nil
}

// aheadBehind returns how many commits target has that running lacks, and
// the reverse.
func (u *Updater) aheadBehind(ctx context.Context, running, target string) (int, int) {
	out, err := u.output(ctx, "git", "rev-list", "--left-right", "--count", running+"..."+target)
	if err != nil {
		return -1, -1
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return -1, -1
	}
	behind, _ := strconv.Atoi(f[0])
	ahead, _ := strconv.Atoi(f[1])
	return ahead, behind
}

func (u *Updater) Commits(ctx context.Context, branch string, n int) ([]Commit, error) {
	if !validRef(branch) {
		return nil, errors.New("نام برنچ نامعتبر است")
	}
	if n <= 0 || n > 200 {
		n = 40
	}
	out, err := u.output(ctx, "git", "log", "-n", strconv.Itoa(n), "--format="+logFormat, u.cfg.Remote+"/"+branch, "--")
	if err != nil {
		return nil, err
	}
	var res []Commit
	for _, line := range strings.Split(out, "\n") {
		if c, ok := parseCommit(line); ok {
			res = append(res, c)
		}
	}
	return res, nil
}

// Resolve turns a branch name or (short) hash into a full commit. A branch
// is looked up on the remote, so it always means the latest pushed commit.
func (u *Updater) Resolve(ctx context.Context, ref string) (Commit, string, error) {
	if !validRef(ref) {
		return Commit{}, "", errors.New("ref نامعتبر است")
	}
	branch := ""
	spec := ref
	if _, err := u.output(ctx, "git", "rev-parse", "--verify", "--quiet", "refs/remotes/"+u.cfg.Remote+"/"+ref); err == nil {
		branch = ref
		spec = "refs/remotes/" + u.cfg.Remote + "/" + ref
	}
	out, err := u.output(ctx, "git", "log", "-1", "--format="+logFormat, spec+"^{commit}", "--")
	if err != nil {
		return Commit{}, "", fmt.Errorf("کامیت یا برنچ %q پیدا نشد", ref)
	}
	c, ok := parseCommit(out)
	if !ok {
		return Commit{}, "", fmt.Errorf("کامیت %q قابل خواندن نیست", ref)
	}
	return c, branch, nil
}

// infraFiles change how the containers themselves are set up; the updater
// cannot apply those, so it warns.
var infraFiles = []string{"docker-compose.yml", "Dockerfile.updater", "cmd/updater", "internal/updater"}

func (u *Updater) changedInfra(ctx context.Context, from, to string) []string {
	if from == "" || from == to {
		return nil
	}
	args := append([]string{"diff", "--name-only", from, to, "--"}, infraFiles...)
	out, err := u.output(ctx, "git", args...)
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func (u *Updater) describe(ctx context.Context, sha string) string {
	out, err := u.output(ctx, "git", "describe", "--tags", "--always", sha)
	if err != nil {
		return short(sha)
	}
	return out
}
