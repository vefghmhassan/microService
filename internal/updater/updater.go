// Package updater builds and deploys new versions of the panel from git.
//
// It runs in its own container (docker-compose service "updater") with the
// docker socket and the repository mounted, because the panel container can
// neither build images nor restart itself. Every version is an image tagged
// with its commit; the running one is also tagged "<image>:current". An
// update builds the new image while the old panel keeps serving, backs up the
// database, swaps the container and waits for the new one to report the
// expected commit. If it does not, the previous image is put back.
package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	// RepoDir is the git checkout. It is mounted at the same path as on the
	// host so docker compose resolves "./data" to the right host directory.
	RepoDir string
	// DataDir is the panel's data directory as seen by the updater.
	DataDir string
	// Service is the compose service of the panel, Image its image name.
	Service string
	Image   string
	Remote  string
	// PanelURL is where the panel answers inside the compose network.
	PanelURL      string
	KeepImages    int
	KeepBackups   int
	MinFreeMB     uint64
	HealthTimeout time.Duration
	// StableFor is how long the new panel must keep answering after its
	// first good health check, to catch a crash shortly after start.
	StableFor time.Duration
}

func (c *Config) defaults() {
	if c.Service == "" {
		c.Service = "microserver"
	}
	if c.Image == "" {
		c.Image = "microserver"
	}
	if c.Remote == "" {
		c.Remote = "origin"
	}
	if c.PanelURL == "" {
		c.PanelURL = "http://microserver:8090"
	}
	if c.KeepImages <= 0 {
		c.KeepImages = 5
	}
	if c.KeepBackups <= 0 {
		c.KeepBackups = 10
	}
	if c.HealthTimeout <= 0 {
		c.HealthTimeout = 2 * time.Minute
	}
	if c.StableFor < 0 {
		c.StableFor = 0
	}
}

// Runner runs an external command. Tests replace it.
type Runner interface {
	Run(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// Op is one update or switch, kept in history.json.
type Op struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // update | switch
	Ref        string    `json:"ref"`
	Branch     string    `json:"branch,omitempty"`
	Target     string    `json:"target,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	From       string    `json:"from,omitempty"`
	User       string    `json:"user,omitempty"`
	Step       string    `json:"step"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	BackupID   string    `json:"backup_id,omitempty"`
	RestoreDB  string    `json:"restore_db,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

const (
	StatusRunning    = "running"
	StatusSuccess    = "success"
	StatusFailed     = "failed"
	StatusRolledBack = "rolled_back"
	StatusCanceled   = "canceled"
	StatusAborted    = "interrupted"
)

// Steps in the order they run; the UI draws them as a progress bar.
const (
	StepFetch    = "fetch"
	StepBuild    = "build"
	StepBackup   = "backup"
	StepSwap     = "swap"
	StepHealth   = "health"
	StepRollback = "rollback"
	StepDone     = "done"
)

var ErrBusy = errors.New("یک عملیات آپدیت دیگر در حال اجراست")

type Updater struct {
	cfg  Config
	run  Runner
	http *http.Client
	now  func() time.Time
	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error

	mu      sync.Mutex
	current *Op
	cancel  context.CancelFunc
	logs    map[string]*opLog
	// fetchedAt rate-limits git fetch for the branch list.
	fetchedAt time.Time
	fetchMu   sync.Mutex
	// freeMB reports free disk space; nil skips the check.
	freeMB func(path string) (uint64, error)
}

func New(cfg Config) *Updater {
	cfg.defaults()
	return &Updater{
		cfg:    cfg,
		run:    execRunner{},
		http:   &http.Client{Timeout: 5 * time.Second},
		now:    time.Now,
		sleep:  sleepCtx,
		logs:   map[string]*opLog{},
		freeMB: freeDiskMB,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (u *Updater) stateDir() string { return filepath.Join(u.cfg.DataDir, "updater") }

// Init marks an operation that was running when the updater stopped as
// interrupted, so the UI does not wait for it forever.
func (u *Updater) Init() error {
	if err := os.MkdirAll(filepath.Join(u.stateDir(), "logs"), 0o700); err != nil {
		return err
	}
	h, err := u.History()
	if err != nil {
		return err
	}
	changed := false
	for i := range h {
		if h[i].Status == StatusRunning {
			h[i].Status = StatusAborted
			h[i].Error = "updater در میانه کار متوقف شد"
			h[i].FinishedAt = u.now()
			changed = true
		}
	}
	if changed {
		return u.saveHistory(h)
	}
	return nil
}

// ---- history ----

func (u *Updater) historyPath() string { return filepath.Join(u.stateDir(), "history.json") }

func (u *Updater) History() ([]Op, error) {
	raw, err := os.ReadFile(u.historyPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h []Op
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("history.json: %w", err)
	}
	return h, nil
}

func (u *Updater) saveHistory(h []Op) error {
	sort.SliceStable(h, func(i, j int) bool { return h[i].StartedAt.After(h[j].StartedAt) })
	if len(h) > 100 {
		h = h[:100]
	}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.stateDir(), 0o700); err != nil {
		return err
	}
	tmp := u.historyPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.historyPath())
}

// save writes op into history (insert or replace by id). Called with u.mu held
// or from the goroutine that owns op.
func (u *Updater) save(op Op) {
	h, err := u.History()
	if err != nil {
		h = nil
	}
	found := false
	for i := range h {
		if h[i].ID == op.ID {
			h[i] = op
			found = true
		}
	}
	if !found {
		h = append(h, op)
	}
	if err := u.saveHistory(h); err != nil {
		fmt.Fprintf(os.Stderr, "updater: save history: %v\n", err)
	}
}

// ---- per-operation log ----

type opLog struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

func (u *Updater) logPath(id string) string {
	return filepath.Join(u.stateDir(), "logs", id+".log")
}

func (u *Updater) openLog(id string) (*opLog, error) {
	if err := os.MkdirAll(filepath.Dir(u.logPath(id)), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(u.logPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l := &opLog{f: f, path: u.logPath(id)}
	u.mu.Lock()
	u.logs[id] = l
	u.mu.Unlock()
	return l, nil
}

func (l *opLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Write(p)
}

func (l *opLog) close() { _ = l.f.Close() }

// ReadLog returns the log from byte offset on, and the new offset.
func (u *Updater) ReadLog(id string, offset int64) ([]byte, int64, error) {
	if strings.ContainsAny(id, `/\.`) || id == "" {
		return nil, 0, errors.New("bad id")
	}
	f, err := os.Open(u.logPath(id))
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	const max = 512 << 10
	n := st.Size() - offset
	if n > max {
		n = max
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	return buf, offset + n, nil
}

// ---- small command helpers ----

// output runs a command and returns its trimmed stdout; stderr goes into the
// error so failures are readable in the UI.
func (u *Updater) output(ctx context.Context, name string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	if err := u.run.Run(ctx, u.cfg.RepoDir, nil, &out, &errb, name, args...); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// stream runs a command writing everything to the operation log.
func (u *Updater) stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	fmt.Fprintf(w, "$ %s %s\n", name, strings.Join(args, " "))
	if err := u.run.Run(ctx, u.cfg.RepoDir, nil, w, w, name, args...); err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
