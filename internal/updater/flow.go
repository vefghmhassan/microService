package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StartUpdate builds ref (a branch or commit) and deploys it in the
// background. force redeploys the version that is already running.
func (u *Updater) StartUpdate(ref, user string, force bool) (*Op, error) {
	ref = strings.TrimSpace(ref)
	if !validRef(ref) {
		return nil, errors.New("برنچ یا کامیت نامعتبر است")
	}
	op := &Op{Kind: "update", Ref: ref, User: user}
	return u.begin(op, func(ctx context.Context, w io.Writer) error { return u.runUpdate(ctx, w, op, force) })
}

// StartSwitch moves to a version already built on this host (downgrade or
// upgrade without building). restore optionally puts a database backup back.
func (u *Updater) StartSwitch(target, restore, user string) (*Op, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("نسخه مقصد مشخص نیست")
	}
	if restore != "" && !validBackupID(restore) {
		return nil, errors.New("شناسه بکاپ نامعتبر است")
	}
	op := &Op{Kind: "switch", Ref: target, User: user, RestoreDB: restore}
	return u.begin(op, func(ctx context.Context, w io.Writer) error { return u.runSwitch(ctx, w, op) })
}

// Cancel stops an operation that is still fetching or building. Once the
// container swap has started it can no longer be canceled.
func (u *Updater) Cancel() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cancel == nil {
		return false
	}
	u.cancel()
	return true
}

func (u *Updater) Busy() *Op {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.current == nil {
		return nil
	}
	c := *u.current
	return &c
}

func (u *Updater) begin(op *Op, fn func(context.Context, io.Writer) error) (*Op, error) {
	u.mu.Lock()
	if u.current != nil {
		u.mu.Unlock()
		return nil, ErrBusy
	}
	op.ID = u.now().UTC().Format("20060102-150405.000")
	op.ID = strings.ReplaceAll(op.ID, ".", "-")
	op.Status = StatusRunning
	op.Step = StepFetch
	op.StartedAt = u.now()
	ctx, cancel := context.WithCancel(context.Background())
	u.current = op
	u.cancel = cancel
	u.mu.Unlock()

	w, err := u.openLog(op.ID)
	if err != nil {
		u.finish(op, err)
		return nil, err
	}
	u.save(*op)
	snapshot := *op
	go func() {
		defer w.close()
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(w, "panic: %v\n", r)
				u.finish(op, fmt.Errorf("panic: %v", r))
			}
		}()
		fmt.Fprintf(w, "== %s %s (by %s) at %s\n", op.Kind, op.Ref, op.User, op.StartedAt.Format(time.RFC3339))
		err := fn(ctx, w)
		if err != nil {
			fmt.Fprintf(w, "!! %v\n", err)
		}
		u.finish(op, err)
		fmt.Fprintf(w, "== finished: %s\n", op.Status)
	}()
	return &snapshot, nil
}

// finish records the outcome. A rolled-back op keeps its status; a canceled
// context becomes "canceled".
func (u *Updater) finish(op *Op, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		if op.Status == StatusRunning {
			op.Status = StatusFailed
			if errors.Is(err, context.Canceled) {
				op.Status = StatusCanceled
			}
		}
		op.Error = err.Error()
	} else if op.Status == StatusRunning {
		op.Status = StatusSuccess
		op.Step = StepDone
	}
	op.FinishedAt = u.now()
	u.save(*op)
	u.current = nil
	u.cancel = nil
	delete(u.logs, op.ID)
}

// setStep records progress so the UI (and history.json, if the updater dies)
// shows where the operation is.
func (u *Updater) setStep(w io.Writer, op *Op, step string) {
	u.mu.Lock()
	op.Step = step
	snap := *op
	u.mu.Unlock()
	fmt.Fprintf(w, "\n---- %s ----\n", step)
	u.save(snap)
}

// noCancel is called right before the container swap: from then on the
// operation must run to the end (or roll back), whatever the user clicks.
func (u *Updater) noCancel(ctx context.Context) context.Context {
	u.mu.Lock()
	u.cancel = nil
	u.mu.Unlock()
	return context.WithoutCancel(ctx)
}

func (u *Updater) runUpdate(ctx context.Context, w io.Writer, op *Op, force bool) error {
	u.setStep(w, op, StepFetch)
	if err := u.Fetch(ctx, 0); err != nil {
		return err
	}
	c, branch, err := u.Resolve(ctx, op.Ref)
	if err != nil {
		return err
	}
	prev, err := u.Current(ctx)
	if err != nil {
		return err
	}
	u.mu.Lock()
	op.Target, op.Branch, op.Subject = c.SHA, branch, c.Subject
	if prev != nil {
		op.From = prev.Commit
	}
	u.mu.Unlock()
	fmt.Fprintf(w, "target %s (%s) %q\n", short(c.SHA), orDash(branch), c.Subject)
	if prev != nil {
		fmt.Fprintf(w, "running %s (%s)\n", orDash(short(prev.Commit)), orDash(prev.Branch))
		if prev.Commit == c.SHA && !force {
			return errors.New("همین نسخه الان در حال اجراست")
		}
	}
	if files := u.changedInfra(ctx, op.From, c.SHA); len(files) > 0 {
		u.warn(w, op, fmt.Sprintf("این فایل‌ها تغییر کرده‌اند و updater آن‌ها را اعمال نمی‌کند: %s — بعد از آپدیت روی سرور اجرا کنید: cd %s && git pull && docker compose up -d --build updater",
			strings.Join(files, ", "), u.cfg.RepoDir))
	}

	u.setStep(w, op, StepBuild)
	tag := u.commitTag(c.SHA)
	if u.imageExists(ctx, tag) && !force {
		fmt.Fprintf(w, "image %s already built, skipping build\n", tag)
	} else {
		if err := u.checkDisk(); err != nil {
			return err
		}
		if err := u.build(ctx, w, c, branch); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return u.deploy(u.noCancel(ctx), w, op, tag, c.SHA, prev, "")
}

func (u *Updater) runSwitch(ctx context.Context, w io.Writer, op *Op) error {
	u.setStep(w, op, StepFetch)
	vs, err := u.Versions(ctx)
	if err != nil {
		return err
	}
	var target, prev *Version
	for i := range vs {
		v := &vs[i]
		if v.Current {
			prev = v
		}
		if target == nil && (v.ID == op.Ref || strings.TrimPrefix(v.ID, "sha256:") == strings.TrimPrefix(op.Ref, "sha256:") ||
			(len(op.Ref) >= 7 && v.Commit != "" && strings.HasPrefix(v.Commit, op.Ref))) {
			target = v
		}
	}
	if target == nil {
		return fmt.Errorf("نسخه %s روی این سرور نیست؛ از «آپدیت» استفاده کنید تا بیلد شود", op.Ref)
	}
	if target.Current && op.RestoreDB == "" {
		return errors.New("همین نسخه الان در حال اجراست")
	}
	u.mu.Lock()
	op.Target, op.Branch, op.Subject = target.Commit, target.Branch, target.Subject
	if prev != nil {
		op.From = prev.Commit
	}
	u.mu.Unlock()
	u.setStep(w, op, StepBuild)
	fmt.Fprintf(w, "image %s already on this host, no build needed\n", short(strings.TrimPrefix(target.ID, "sha256:")))
	return u.deploy(u.noCancel(ctx), w, op, target.ID, target.Commit, prev, op.RestoreDB)
}

// deploy backs up, swaps the container to ref and checks it, rolling back to
// prev when the new version does not come up.
func (u *Updater) deploy(ctx context.Context, w io.Writer, op *Op, ref, expect string, prev *Version, restore string) error {
	u.setStep(w, op, StepBackup)
	fromCommit := ""
	if prev != nil {
		fromCommit = prev.Commit
	}
	bid, err := u.backupDB(ctx, w, fromCommit, op.Kind+" "+op.Ref)
	if err != nil {
		return err
	}
	u.mu.Lock()
	op.BackupID = bid
	u.mu.Unlock()
	// Keep the previous image reachable by a tag of its own: once :current
	// moves it would otherwise be dangling and could be pruned.
	if prev != nil {
		keep := u.cfg.Image + ":prev-" + u.now().UTC().Format("20060102150405")
		if prev.Commit != "" {
			keep = u.commitTag(prev.Commit)
		}
		if err := u.stream(ctx, w, "docker", "tag", prev.ID, keep); err != nil {
			return err
		}
	}

	u.setStep(w, op, StepSwap)
	restored := false
	if restore != "" {
		if err := u.compose(ctx, w, "stop", u.cfg.Service); err != nil {
			return err
		}
		if err := u.restoreDB(w, restore); err != nil {
			// The old version is stopped: start it again before giving up.
			_ = u.compose(ctx, w, "start", u.cfg.Service)
			return err
		}
		restored = true
	}
	swapErr := u.activate(ctx, w, ref)

	healthErr := swapErr
	if swapErr == nil {
		u.setStep(w, op, StepHealth)
		healthErr = u.waitHealthy(ctx, w, expect)
	}
	if healthErr == nil {
		u.pruneImages(ctx, w, nonEmpty(prevID(prev), prevCommit(prev))...)
		return nil
	}

	fmt.Fprintf(w, "!! new version is not healthy: %v\n", healthErr)
	u.dumpLogs(ctx, w)
	if prev == nil {
		return fmt.Errorf("نسخه جدید بالا نیامد و نسخه قبلی برای برگشت وجود ندارد: %w", healthErr)
	}
	u.setStep(w, op, StepRollback)
	if restored && bid != "" {
		_ = u.compose(ctx, w, "stop", u.cfg.Service)
		if err := u.restoreDB(w, bid); err != nil {
			fmt.Fprintf(w, "!! restore pre-switch backup: %v\n", err)
		}
	}
	if err := u.activate(ctx, w, prev.ID); err != nil {
		return fmt.Errorf("برگشت به نسخه قبلی هم ناموفق بود: %v (خطای اصلی: %v)", err, healthErr)
	}
	if err := u.waitHealthy(ctx, w, prev.Commit); err != nil {
		return fmt.Errorf("بعد از برگشت به نسخه قبلی هم پنل سالم نیست: %v (خطای اصلی: %v)", err, healthErr)
	}
	u.mu.Lock()
	op.Status = StatusRolledBack
	u.mu.Unlock()
	return fmt.Errorf("نسخه جدید سالم بالا نیامد و به نسخه قبلی (%s) برگشت داده شد: %w", short(prev.Commit), healthErr)
}

func prevID(v *Version) string {
	if v == nil {
		return ""
	}
	return v.ID
}

func prevCommit(v *Version) string {
	if v == nil {
		return ""
	}
	return v.Commit
}

func nonEmpty(s ...string) []string {
	var r []string
	for _, x := range s {
		if x != "" {
			r = append(r, x)
		}
	}
	return r
}

// PanelVersion asks the running panel what it is.
func (u *Updater) PanelVersion(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.cfg.PanelURL+"/api/v1/version", nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var v map[string]string
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// waitHealthy waits for the panel to report commit expect (any commit when
// expect is empty), then checks again after StableFor.
func (u *Updater) waitHealthy(ctx context.Context, w io.Writer, expect string) error {
	deadline := u.now().Add(u.cfg.HealthTimeout)
	var last error
	check := func() error {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		v, err := u.PanelVersion(cctx)
		if err != nil {
			return err
		}
		if expect != "" && v["commit"] != expect {
			return fmt.Errorf("panel reports commit %q, expected %s", short(v["commit"]), short(expect))
		}
		return nil
	}
	for i := 0; ; i++ {
		if last = check(); last == nil {
			break
		}
		if i%5 == 0 {
			fmt.Fprintf(w, "waiting for panel: %v\n", last)
		}
		if u.now().After(deadline) {
			return fmt.Errorf("timeout after %s: %w", u.cfg.HealthTimeout, last)
		}
		if err := u.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "panel answers with the expected version\n")
	if u.cfg.StableFor > 0 {
		if err := u.sleep(ctx, u.cfg.StableFor); err != nil {
			return err
		}
		if err := check(); err != nil {
			return fmt.Errorf("panel stopped answering after start: %w", err)
		}
		fmt.Fprintf(w, "panel still healthy after %s\n", u.cfg.StableFor)
	}
	return nil
}

func (u *Updater) dumpLogs(ctx context.Context, w io.Writer) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fmt.Fprintf(w, "---- last panel logs ----\n")
	_ = u.run.Run(cctx, u.cfg.RepoDir, nil, w, w, "docker", "compose", "logs", "--no-color", "--tail", "60", u.cfg.Service)
}

func (u *Updater) checkDisk() error {
	if u.cfg.MinFreeMB == 0 || u.freeMB == nil {
		return nil
	}
	free, err := u.freeMB("/")
	if err != nil {
		return nil
	}
	if free < u.cfg.MinFreeMB {
		return fmt.Errorf("فضای دیسک کافی نیست: %d MB آزاد، حداقل %d MB لازم است", free, u.cfg.MinFreeMB)
	}
	return nil
}

func (u *Updater) warn(w io.Writer, op *Op, msg string) {
	fmt.Fprintf(w, "WARNING: %s\n", msg)
	u.mu.Lock()
	op.Warnings = append(op.Warnings, msg)
	u.mu.Unlock()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
