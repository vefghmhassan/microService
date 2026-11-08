// Command updater is the panel's self-update sidecar. See internal/updater.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"microserver/internal/updater"
)

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(env(k, "")); err == nil {
		return n
	}
	return def
}

func main() {
	repo := env("REPO_DIR", env("HOST_REPO_DIR", "."))
	data := env("PANEL_DATA_DIR", filepath.Join(repo, "data"))
	cfg := updater.Config{
		RepoDir:       repo,
		DataDir:       data,
		Service:       env("PANEL_SERVICE", "microserver"),
		Image:         env("PANEL_IMAGE", "microserver"),
		Remote:        env("GIT_REMOTE", "origin"),
		PanelURL:      strings.TrimRight(env("PANEL_URL", "http://microserver:8090"), "/"),
		KeepImages:    envInt("KEEP_IMAGES", 5),
		KeepBackups:   envInt("KEEP_BACKUPS", 10),
		MinFreeMB:     uint64(envInt("MIN_FREE_MB", 1500)),
		HealthTimeout: time.Duration(envInt("HEALTH_TIMEOUT_SEC", 120)) * time.Second,
		StableFor:     time.Duration(envInt("HEALTH_STABLE_SEC", 10)) * time.Second,
	}
	u := updater.New(cfg)
	if err := u.Init(); err != nil {
		log.Fatalf("updater: %v", err)
	}
	srv := &http.Server{
		Addr:              env("LISTEN", ":8091"),
		Handler:           u.Handler(filepath.Join(data, "updater.token")),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("updater: repo %s, data %s, listening on %s", repo, data, srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
