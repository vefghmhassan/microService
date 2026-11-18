package updater

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Handler serves the updater API. Only the panel talks to it, with the token
// from TokenFile (shared through the data volume).
func (u *Updater) Handler(tokenFile string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", u.hStatus)
	mux.HandleFunc("GET /branches", u.hBranches)
	mux.HandleFunc("GET /commits", u.hCommits)
	mux.HandleFunc("GET /versions", u.hVersions)
	mux.HandleFunc("GET /backups", u.hBackups)
	mux.HandleFunc("GET /history", u.hHistory)
	mux.HandleFunc("GET /ops/{id}/log", u.hLog)
	mux.HandleFunc("POST /update", u.hUpdate)
	mux.HandleFunc("POST /switch", u.hSwitch)
	mux.HandleFunc("POST /cancel", u.hCancel)
	return auth(tokenFile, mux)
}

func auth(tokenFile string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read on every request: the panel creates the file on its first boot,
		// possibly after the updater started.
		tok := strings.TrimSpace(os.Getenv("UPDATER_TOKEN"))
		if tok == "" {
			raw, err := os.ReadFile(tokenFile)
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, "updater token not found yet ("+tokenFile+"); start the panel first")
				return
			}
			tok = strings.TrimSpace(string(raw))
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" || subtle.ConstantTimeCompare([]byte(got), []byte(tok)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusResp struct {
	Busy         bool              `json:"busy"`
	Op           *Op               `json:"op"`
	Current      *Version          `json:"current"`
	Panel        map[string]string `json:"panel,omitempty"`
	PanelError   string            `json:"panel_error,omitempty"`
	RepoDir      string            `json:"repo_dir"`
	Remote       string            `json:"remote"`
	RemoteURL    string            `json:"remote_url"`
	CheckedOut   string            `json:"checked_out"`
	ServerTime   time.Time         `json:"server_time"`
	DockerErrors string            `json:"docker_error,omitempty"`
}

func (u *Updater) hStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	res := statusResp{RepoDir: u.cfg.RepoDir, Remote: u.cfg.Remote, ServerTime: u.now()}
	if op := u.Busy(); op != nil {
		res.Busy, res.Op = true, op
	} else if h, _ := u.History(); len(h) > 0 {
		res.Op = &h[0]
	}
	cur, err := u.Current(ctx)
	if err != nil {
		res.DockerErrors = err.Error()
	}
	res.Current = cur
	if v, err := u.PanelVersion(ctx); err == nil {
		res.Panel = v
	} else {
		res.PanelError = err.Error()
	}
	res.RemoteURL, _ = u.output(ctx, "git", "remote", "get-url", u.cfg.Remote)
	res.CheckedOut, _ = u.output(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	writeJSON(w, http.StatusOK, res)
}

// runningCommit is the commit of the deployed image, or of the panel itself
// on a host whose image has no labels yet.
func (u *Updater) runningCommit(ctx context.Context) string {
	if cur, err := u.Current(ctx); err == nil && cur != nil && cur.Commit != "" {
		return cur.Commit
	}
	if v, err := u.PanelVersion(ctx); err == nil {
		return v["commit"]
	}
	return ""
}

func (u *Updater) hBranches(w http.ResponseWriter, r *http.Request) {
	maxAge := time.Minute
	if r.URL.Query().Get("refresh") == "1" {
		maxAge = 0
	}
	fetchErr := ""
	if err := u.Fetch(r.Context(), maxAge); err != nil {
		// Still list what we have: GitHub may simply be unreachable.
		fetchErr = err.Error()
	}
	bs, err := u.Branches(r.Context(), u.runningCommit(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bs == nil {
		bs = []Branch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": bs, "fetch_error": fetchErr, "fetched_at": u.fetchedAt})
}

func (u *Updater) hCommits(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	cs, err := u.Commits(r.Context(), r.URL.Query().Get("branch"), n)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if cs == nil {
		cs = []Commit{}
	}
	writeJSON(w, http.StatusOK, cs)
}

func (u *Updater) hVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := u.Versions(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if vs == nil {
		vs = []Version{}
	}
	writeJSON(w, http.StatusOK, vs)
}

func (u *Updater) hBackups(w http.ResponseWriter, r *http.Request) {
	bs, err := u.Backups()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if bs == nil {
		bs = []Backup{}
	}
	writeJSON(w, http.StatusOK, bs)
}

func (u *Updater) hHistory(w http.ResponseWriter, r *http.Request) {
	h, err := u.History()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(h) > 30 {
		h = h[:30]
	}
	if h == nil {
		h = []Op{}
	}
	writeJSON(w, http.StatusOK, h)
}

func (u *Updater) hLog(w http.ResponseWriter, r *http.Request) {
	off, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	data, next, err := u.ReadLog(r.PathValue("id"), off)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": string(data), "offset": next})
}

func (u *Updater) hUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref   string `json:"ref"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	u.started(w, r)(u.StartUpdate(req.Ref, r.Header.Get("X-Panel-User"), req.Force))
}

func (u *Updater) hSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target    string `json:"target"`
		RestoreDB string `json:"restore_db"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	u.started(w, r)(u.StartSwitch(req.Target, req.RestoreDB, r.Header.Get("X-Panel-User")))
}

func (u *Updater) started(w http.ResponseWriter, r *http.Request) func(*Op, error) {
	return func(op *Op, err error) {
		switch {
		case errors.Is(err, ErrBusy):
			writeErr(w, http.StatusConflict, err.Error())
		case err != nil:
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			writeJSON(w, http.StatusAccepted, op)
		}
	}
}

func (u *Updater) hCancel(w http.ResponseWriter, r *http.Request) {
	if !u.Cancel() {
		writeErr(w, http.StatusConflict, "عملیاتی برای لغو نیست (یا از مرحله جایگزینی گذشته)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
