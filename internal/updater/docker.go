package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Version is a panel image kept on this host.
type Version struct {
	ID      string    `json:"id"`
	Tags    []string  `json:"tags"`
	Commit  string    `json:"commit"`
	Short   string    `json:"short"`
	Branch  string    `json:"branch"`
	Subject string    `json:"subject"`
	Version string    `json:"version"`
	Built   string    `json:"built"`
	Created time.Time `json:"created"`
	SizeMB  int64     `json:"size_mb"`
	Current bool      `json:"current"`
}

func (u *Updater) currentTag() string { return u.cfg.Image + ":current" }
func (u *Updater) commitTag(sha string) string {
	return u.cfg.Image + ":" + short(sha)
}

type inspected struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Created  string   `json:"Created"`
	Size     int64    `json:"Size"`
	Config   struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (i inspected) version() Version {
	l := i.Config.Labels
	c, _ := time.Parse(time.RFC3339Nano, i.Created)
	v := Version{
		ID: i.ID, Tags: i.RepoTags, Commit: l["ms.commit"], Branch: l["ms.branch"],
		Subject: l["ms.subject"], Version: l["ms.version"], Built: l["ms.built"],
		Created: c, SizeMB: i.Size >> 20,
	}
	v.Short = short(v.Commit)
	return v
}

func (u *Updater) inspect(ctx context.Context, refs ...string) ([]inspected, error) {
	out, err := u.output(ctx, "docker", append([]string{"image", "inspect"}, refs...)...)
	if err != nil {
		return nil, err
	}
	var res []inspected
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("docker image inspect: %w", err)
	}
	return res, nil
}

// Current returns the image tagged :current, or nil on a host that has not
// been set up by the new compose file yet.
func (u *Updater) Current(ctx context.Context) (*Version, error) {
	res, err := u.inspect(ctx, u.currentTag())
	if err != nil {
		if strings.Contains(err.Error(), "No such image") || strings.Contains(err.Error(), "no such image") {
			return nil, nil
		}
		return nil, err
	}
	if len(res) == 0 {
		return nil, nil
	}
	v := res[0].version()
	v.Current = true
	return &v, nil
}

// Versions lists the panel images on this host, newest first.
func (u *Updater) Versions(ctx context.Context) ([]Version, error) {
	ids, err := u.output(ctx, "docker", "image", "ls", "--no-trunc", "-q", "--filter", "label=ms.app="+u.cfg.Image)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var refs []string
	for _, id := range strings.Fields(ids) {
		if !seen[id] {
			seen[id] = true
			refs = append(refs, id)
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	list, err := u.inspect(ctx, refs...)
	if err != nil {
		return nil, err
	}
	cur, _ := u.Current(ctx)
	res := make([]Version, 0, len(list))
	for _, i := range list {
		v := i.version()
		v.Current = cur != nil && cur.ID == v.ID
		res = append(res, v)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Created.After(res[j].Created) })
	return res, nil
}

func (u *Updater) imageExists(ctx context.Context, ref string) bool {
	_, err := u.output(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil
}

// build streams `git archive <sha>` into `docker build -`: the working tree is
// never touched, so local edits on the server cannot leak into an image.
func (u *Updater) build(ctx context.Context, w io.Writer, c Commit, branch string) error {
	pr, pw := io.Pipe()
	archErr := make(chan error, 1)
	go func() {
		err := u.run.Run(ctx, u.cfg.RepoDir, nil, pw, w, "git", "archive", "--format=tar", c.SHA)
		_ = pw.CloseWithError(err)
		archErr <- err
	}()
	subject := c.Subject
	if len(subject) > 200 {
		subject = subject[:200]
	}
	args := []string{"build", "--progress=plain",
		"--build-arg", "VERSION=" + u.describe(ctx, c.SHA),
		"--build-arg", "COMMIT=" + c.SHA,
		"--build-arg", "BRANCH=" + branch,
		"--build-arg", "BUILD_TIME=" + u.now().UTC().Format(time.RFC3339),
		"--build-arg", "SUBJECT=" + subject,
		"-t", u.commitTag(c.SHA), "-"}
	fmt.Fprintf(w, "$ git archive %s | docker %s\n", short(c.SHA), strings.Join(args, " "))
	err := u.run.Run(ctx, u.cfg.RepoDir, pr, w, w, "docker", args...)
	_ = pr.Close()
	if aerr := <-archErr; aerr != nil && err == nil {
		err = fmt.Errorf("git archive: %w", aerr)
	}
	if err != nil {
		return fmt.Errorf("بیلد ناموفق بود: %w", err)
	}
	return nil
}

// activate points :current at ref and recreates the panel container.
func (u *Updater) activate(ctx context.Context, w io.Writer, ref string) error {
	if err := u.stream(ctx, w, "docker", "tag", ref, u.currentTag()); err != nil {
		return err
	}
	return u.compose(ctx, w, "up", "-d", "--no-deps", "--no-build", "--force-recreate", u.cfg.Service)
}

func (u *Updater) compose(ctx context.Context, w io.Writer, args ...string) error {
	return u.stream(ctx, w, "docker", append([]string{"compose"}, args...)...)
}

// pruneImages keeps the newest KeepImages versions plus the ones in keep.
func (u *Updater) pruneImages(ctx context.Context, w io.Writer, keep ...string) {
	vs, err := u.Versions(ctx)
	if err != nil {
		fmt.Fprintf(w, "! prune: %v\n", err)
		return
	}
	keepID := map[string]bool{}
	for _, k := range keep {
		keepID[k] = true
	}
	kept := 0
	for _, v := range vs {
		if v.Current || keepID[v.ID] || keepID[v.Commit] {
			continue
		}
		kept++
		if kept <= u.cfg.KeepImages {
			continue
		}
		for _, t := range v.Tags {
			if t == u.currentTag() {
				continue
			}
			if _, err := u.output(ctx, "docker", "image", "rm", t); err != nil {
				fmt.Fprintf(w, "! remove %s: %v\n", t, err)
			} else {
				fmt.Fprintf(w, "removed old version %s\n", t)
			}
		}
	}
	// Unreferenced layers of old builds; failures are harmless.
	_, _ = u.output(ctx, "docker", "image", "prune", "-f", "--filter", "label=ms.app="+u.cfg.Image)
}
