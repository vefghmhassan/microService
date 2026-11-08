package updater

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	oldSHA = "1111111111111111111111111111111111111111"
	newSHA = "2222222222222222222222222222222222222222"
)

// fakeHost plays git and docker. The panel answers with whatever commit the
// :current tag points at, unless broken says the new image cannot start.
type fakeHost struct {
	mu       sync.Mutex
	cmds     []string
	current  string // commit behind :current
	images   map[string]string
	broken   map[string]bool // commit -> panel does not come up
	buildErr error
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		current: oldSHA,
		images:  map[string]string{"sha256:old": oldSHA},
		broken:  map[string]bool{},
	}
}

func (f *fakeHost) idOf(commit string) string {
	for id, c := range f.images {
		if c == commit {
			return id
		}
	}
	return ""
}

func (f *fakeHost) Run(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	line := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.cmds = append(f.cmds, line)
	f.mu.Unlock()
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	if strings.HasPrefix(line, "git archive") {
		// Writes into a pipe the docker build reads: must not hold the lock.
		_, _ = stdout.Write([]byte("tar"))
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasPrefix(line, "git fetch"):
		return nil
	case strings.HasPrefix(line, "git rev-parse --verify --quiet refs/remotes/origin/develop"):
		return nil
	case strings.HasPrefix(line, "git rev-parse"):
		return errors.New("unknown ref")
	case strings.HasPrefix(line, "git log -1"):
		fmt.Fprintf(stdout, "%s\tdev\t2026-11-08T10:00:00Z\tnew feature\n", newSHA)
		return nil
	case strings.HasPrefix(line, "git archive"):
		_, _ = stdout.Write([]byte("tar"))
		return nil
	case strings.HasPrefix(line, "git describe"):
		fmt.Fprint(stdout, "v1-2-g2222")
		return nil
	case strings.HasPrefix(line, "git diff"):
		return nil
	case strings.HasPrefix(line, "docker build"):
		if f.buildErr != nil {
			return f.buildErr
		}
		f.images["sha256:new"] = newSHA
		return nil
	case strings.HasPrefix(line, "docker image inspect --format"):
		ref := args[len(args)-1]
		for _, c := range f.images {
			if ref == "microserver:"+short(c) {
				return nil
			}
		}
		return errors.New("No such image")
	case strings.HasPrefix(line, "docker image inspect"):
		var out []map[string]any
		for _, ref := range args[2:] {
			id := ref
			if ref == "microserver:current" {
				id = f.idOf(f.current)
			}
			c, ok := f.images[id]
			if !ok {
				return errors.New("Error: No such image: " + ref)
			}
			out = append(out, map[string]any{
				"Id": id, "RepoTags": []string{"microserver:" + short(c)}, "Created": time.Now().Format(time.RFC3339Nano),
				"Config": map[string]any{"Labels": map[string]string{"ms.app": "microserver", "ms.commit": c, "ms.branch": "develop"}},
			})
		}
		return json.NewEncoder(stdout).Encode(out)
	case strings.HasPrefix(line, "docker image ls"):
		for id := range f.images {
			fmt.Fprintln(stdout, id)
		}
		return nil
	case strings.HasPrefix(line, "docker tag"):
		src, dst := args[1], args[2]
		if dst == "microserver:current" {
			if c, ok := f.images[src]; ok {
				f.current = c
			} else {
				for _, c := range f.images {
					if src == "microserver:"+short(c) {
						f.current = c
					}
				}
			}
		}
		return nil
	case strings.HasPrefix(line, "docker"):
		return nil
	}
	return fmt.Errorf("unexpected command %q", line)
}

func (f *fakeHost) ran(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func newTestUpdater(t *testing.T, f *fakeHost) *Updater {
	t.Helper()
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		c, broken := f.current, f.broken[f.current]
		f.mu.Unlock()
		if broken {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"commit": c})
	}))
	t.Cleanup(panel.Close)
	dir := t.TempDir()
	u := New(Config{RepoDir: dir, DataDir: filepath.Join(dir, "data"), PanelURL: panel.URL, HealthTimeout: time.Second})
	u.run = f
	u.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	u.freeMB = nil
	if err := u.Init(); err != nil {
		t.Fatal(err)
	}
	return u
}

func waitOp(t *testing.T, u *Updater, id string) Op {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if u.Busy() == nil {
			h, err := u.History()
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range h {
				if op.ID == id {
					return op
				}
			}
			t.Fatalf("op %s not in history", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return Op{}
}

func TestUpdateSucceeds(t *testing.T) {
	f := newFakeHost()
	u := newTestUpdater(t, f)
	op, err := u.StartUpdate("develop", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	got := waitOp(t, u, op.ID)
	if got.Status != StatusSuccess || got.Target != newSHA || got.From != oldSHA || got.Branch != "develop" {
		t.Fatalf("op = %+v", got)
	}
	if f.current != newSHA {
		t.Fatalf(":current = %s, want new", f.current)
	}
	if f.ran("docker build") != 1 || f.ran("docker compose up") != 1 {
		t.Fatalf("commands: %v", f.cmds)
	}
	if _, err := u.StartUpdate("develop", "admin", false); err != nil {
		t.Fatal(err)
	}
	// Second update to the same commit: refused, nothing rebuilt or swapped.
	for u.Busy() != nil {
		time.Sleep(time.Millisecond)
	}
	if f.ran("docker build") != 1 || f.ran("docker compose up") != 1 {
		t.Fatalf("same version was redeployed: %v", f.cmds)
	}
}

func TestUnhealthyVersionRollsBack(t *testing.T) {
	f := newFakeHost()
	f.broken[newSHA] = true
	u := newTestUpdater(t, f)
	op, err := u.StartUpdate("develop", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	got := waitOp(t, u, op.ID)
	if got.Status != StatusRolledBack || got.Step != StepRollback {
		t.Fatalf("op = %+v", got)
	}
	if f.current != oldSHA {
		t.Fatalf(":current = %s, want old after rollback", f.current)
	}
	if f.ran("docker compose up") != 2 {
		t.Fatalf("want swap + rollback, got %v", f.cmds)
	}
}

func TestBuildFailureKeepsOldVersion(t *testing.T) {
	f := newFakeHost()
	f.buildErr = errors.New("exit status 1")
	u := newTestUpdater(t, f)
	op, err := u.StartUpdate("develop", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	got := waitOp(t, u, op.ID)
	if got.Status != StatusFailed || got.Step != StepBuild {
		t.Fatalf("op = %+v", got)
	}
	if f.ran("docker compose") != 0 || f.ran("docker tag") != 0 || f.current != oldSHA {
		t.Fatalf("panel was touched after a failed build: %v", f.cmds)
	}
	log, _, err := u.ReadLog(op.ID, 0)
	if err != nil || !strings.Contains(string(log), "exit status 1") {
		t.Fatalf("log = %q, %v", log, err)
	}
}

func TestSwitchToBuiltVersion(t *testing.T) {
	f := newFakeHost()
	f.images["sha256:new"] = newSHA
	f.current = newSHA
	u := newTestUpdater(t, f)
	op, err := u.StartSwitch("11111111", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	got := waitOp(t, u, op.ID)
	if got.Status != StatusSuccess || f.current != oldSHA {
		t.Fatalf("op = %+v current=%s", got, f.current)
	}
	if f.ran("docker build") != 0 {
		t.Fatal("switch must not build")
	}
}

func TestOnlyOneOperationAtATime(t *testing.T) {
	f := newFakeHost()
	u := newTestUpdater(t, f)
	block := make(chan struct{})
	var started atomic.Bool
	u.run = runnerFunc(func(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		if !started.Swap(true) {
			<-block
		}
		return f.Run(ctx, dir, stdin, stdout, stderr, name, args...)
	})
	op, err := u.StartUpdate("develop", "a", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.StartUpdate("develop", "b", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("second start: %v, want ErrBusy", err)
	}
	close(block)
	waitOp(t, u, op.ID)
}

type runnerFunc func(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error

func (r runnerFunc) Run(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	return r(ctx, dir, stdin, stdout, stderr, name, args...)
}

func TestBackupAndRestore(t *testing.T) {
	dir := t.TempDir()
	u := New(Config{RepoDir: dir, DataDir: dir})
	dbPath := filepath.Join(dir, dbFile)
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec := func(q string) {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mustExec("PRAGMA journal_mode=WAL")
	mustExec("CREATE TABLE t (v TEXT)")
	mustExec("INSERT INTO t VALUES ('before')")
	if err := os.WriteFile(filepath.Join(dir, "master.key"), []byte("k1"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := u.backupDB(context.Background(), io.Discard, oldSHA, "test")
	if err != nil || id == "" {
		t.Fatalf("backup: %q %v", id, err)
	}
	mustExec("INSERT INTO t VALUES ('after')")
	db.Close()
	if err := u.restoreDB(io.Discard, id); err != nil {
		t.Fatal(err)
	}
	db2, _ := sql.Open("sqlite", "file:"+dbPath)
	defer db2.Close()
	var n int
	if err := db2.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after restore = %d, %v", n, err)
	}
	bs, _ := u.Backups()
	if len(bs) != 1 || bs[0].Commit != oldSHA {
		t.Fatalf("backups = %+v", bs)
	}
	if err := u.restoreDB(io.Discard, "../etc"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestValidRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"develop": true, "feature/x-1": true, "a1b2c3d": true,
		"--upload-pack=x": false, "": false, "a..b": false, "-x": false, "a b": false,
	} {
		if validRef(ref) != want {
			t.Errorf("validRef(%q) = %v", ref, !want)
		}
	}
}
