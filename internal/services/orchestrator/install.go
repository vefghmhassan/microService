package orchestrator

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/secret"
	"microserver/internal/services/settings"
	"microserver/internal/services/sshx"
)

const installResultPath = "/etc/x-ui/install-result.env"

// InstallScript is the shell run over SSH. 3x-ui's installer skips every
// prompt when XUI_NONINTERACTIVE=1 (or stdin is not a TTY) and takes the panel
// credentials from XUI_* variables, writing what it used to install-result.env.
func InstallScript(installURL, version, user, pass string, port int, basePath string) string {
	q := sshx.ShellQuote
	return strings.Join([]string{
		"set -e",
		"export DEBIAN_FRONTEND=noninteractive XUI_NONINTERACTIVE=1 XUI_SSL_MODE=none",
		"export XUI_USERNAME=" + q(user) + " XUI_PASSWORD=" + q(pass),
		"export XUI_PANEL_PORT=" + strconv.Itoa(port) + " XUI_WEB_BASE_PATH=" + q(basePath),
		// A fresh image runs apt in the background; the installer would fail on the lock.
		"for i in $(seq 1 120); do fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock >/dev/null 2>&1 || break; sleep 3; done",
		"curl -fsSL " + q(installURL) + " -o /tmp/x-ui-install.sh",
		"bash /tmp/x-ui-install.sh " + q(version) + " </dev/null",
	}, "\n")
}

func randomInt(min, max int64) int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(max-min))
	return min + n.Int64()
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9_\-\.]{24,}`)

// setPanelListen binds the 3x-ui web panel to every interface (open it in a
// browser at http://IP:port/path/) or to localhost only (reachable through the
// SSH tunnel alone). This microServer always talks to it through the tunnel.
func (o *Orchestrator) setPanelListen(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote, public bool) error {
	ip := "127.0.0.1"
	if public {
		ip = "0.0.0.0"
	}
	cmd := fmt.Sprintf("x-ui setting -listenIP %s >/dev/null 2>&1 && (x-ui restart >/dev/null 2>&1 || systemctl restart x-ui)", ip)
	if public {
		// Open the port when a host firewall is active (cloud images usually have none).
		cmd += fmt.Sprintf("; if command -v ufw >/dev/null && ufw status | grep -q 'Status: active'; then ufw allow %d/tcp >/dev/null; fi; true", s.XUIPort)
	}
	if _, err := remote.Run(ctx, cmd, nil); err != nil {
		return fmt.Errorf("could not set the panel listen address to %s: %w", ip, err)
	}
	s.XUIPublic = public
	o.save(r, s, "xui_public")
	if public {
		r.Info("3x-ui panel is open at %s", PanelURL(s))
	} else {
		r.Info("3x-ui panel listens on localhost only")
	}
	return nil
}

// PanelURL is the browser address of a server's 3x-ui panel.
func PanelURL(s *models.Server) string {
	scheme := s.XUIScheme
	if scheme == "" {
		scheme = "http"
	}
	bp := strings.Trim(s.XUIBasePath, "/")
	if bp != "" {
		bp += "/"
	}
	return fmt.Sprintf("%s://%s:%d/%s", scheme, s.IPv4, s.XUIPort, bp)
}

// panelAccess switches an installed server's panel between public and local.
func (o *Orchestrator) panelAccess(ctx context.Context, r *jobs.Run) error {
	var p struct {
		Public bool `json:"public"`
	}
	_ = r.Params(&p)
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.XUIPort == 0 {
		return fmt.Errorf("3x-ui is not installed on this server")
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	if err := o.setPanelListen(ctx, r, s, remote, p.Public); err != nil {
		return err
	}
	return o.waitPanel(ctx, r, s, o.PanelFor(s, remote))
}

// waitPanel waits until the panel answers through the tunnel.
func (o *Orchestrator) waitPanel(ctx context.Context, r *jobs.Run, s *models.Server, panel Panel) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		st, err := panel.Status(ctx)
		if err == nil {
			if st.Xray.Version != "" {
				s.XrayVersion = st.Xray.Version
				o.save(r, s, "xray_version")
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("3x-ui did not come up: %w", err)
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// installPanel installs (or reinstalls at another version) 3x-ui and stores
// the credentials and API token it ends up with.
func (o *Orchestrator) installPanel(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote, version string) error {
	version = firstNonEmpty(version, s.WantPanelVer, o.Settings.Get(settings.XUIVersion))
	if s.XUIPort == 0 {
		s.XUIPort = int(randomInt(20000, 60000))
		s.XUIBasePath = randomSuffix(8)
		s.XUIUsername = "ms" + randomSuffix(4)
		s.XUIPassword = secretString(randomSuffix(12))
		s.XUIScheme = "http"
	}
	pass := string(s.XUIPassword)
	if pass == "" {
		pass = randomSuffix(12)
		s.XUIPassword = secretString(pass)
	}

	r.Info("waiting for cloud-init to finish")
	_, _ = remote.Run(ctx, "cloud-init status --wait >/dev/null 2>&1 || true", nil)

	r.Info("installing 3x-ui %s", version)
	script := InstallScript(o.Settings.Get(settings.XUIInstallURL), version, s.XUIUsername, pass, s.XUIPort, s.XUIBasePath)
	installCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if _, err := remote.Run(installCtx, "bash -c "+sshx.ShellQuote(script), func(line string) {
		if strings.TrimSpace(line) != "" {
			r.Logf("out", "%s", line)
		}
	}); err != nil {
		return fmt.Errorf("3x-ui install: %w", err)
	}

	env := map[string]string{}
	if raw, err := remote.ReadFile(ctx, installResultPath); err == nil {
		env = sshx.ParseEnv(raw)
	} else {
		r.Logf("warn", "%s not found; using the values we asked for", installResultPath)
	}
	if v := env["XUI_PANEL_PORT"]; v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			s.XUIPort = p
		}
	}
	if v := env["XUI_WEB_BASE_PATH"]; v != "" {
		s.XUIBasePath = strings.Trim(v, "/")
	}
	if v := env["XUI_USERNAME"]; v != "" {
		s.XUIUsername = v
	}
	if v := env["XUI_PASSWORD"]; v != "" {
		s.XUIPassword = secretString(v)
	}
	token := env["XUI_API_TOKEN"]
	if token == "" {
		out, err := remote.Run(ctx, "x-ui setting -getApiToken 2>&1", nil)
		if err == nil {
			if m := tokenRe.FindAllString(out, -1); len(m) > 0 {
				token = m[len(m)-1]
			}
		}
	}
	if token == "" {
		return fmt.Errorf("could not obtain a 3x-ui API token (is %s at least v3.x?)", version)
	}
	s.XUIToken = secretString(token)
	if strings.HasPrefix(env["XUI_ACCESS_URL"], "https://") {
		s.XUIScheme = "https"
	}

	if err := o.setPanelListen(ctx, r, s, remote, o.Settings.Bool(settings.XUIPublic)); err != nil {
		r.Logf("warn", "%v", err)
	}

	s.PanelVersion = version
	o.save(r, s, "xui_port", "xui_base_path", "xui_username", "xui_password", "xui_token", "xui_scheme", "panel_version")

	panel := o.PanelFor(s, remote)
	if err := o.waitPanel(ctx, r, s, panel); err != nil {
		return err
	}
	r.Info("3x-ui %s is up (xray %s)", version, s.XrayVersion)

	// Subscriptions are not used: configs reach users through vpn-pannel.
	changed, err := panel.DisableSubscription(ctx)
	switch {
	case err != nil:
		r.Logf("warn", "could not turn off the subscription service: %v", err)
	case changed:
		r.Info("subscription service turned off; restarting the panel")
		if err := panel.RestartPanel(ctx); err != nil {
			r.Logf("warn", "restart panel: %v", err)
		}
		// Give the old process time to exit before polling the new one.
		_ = sleep(ctx, o.RetryDelay/6)
		if err := o.waitPanel(ctx, r, s, panel); err != nil {
			return err
		}
	}

	if want := firstNonEmpty(s.WantXrayVer, o.Settings.Get(settings.XrayVersion)); want != "" &&
		strings.TrimPrefix(want, "v") != strings.TrimPrefix(s.XrayVersion, "v") {
		r.Info("switching xray core to %s", want)
		if err := panel.InstallXray(ctx, want); err != nil {
			r.Logf("warn", "xray %s: %v", want, err)
		} else {
			s.XrayVersion = strings.TrimPrefix(want, "v")
			o.save(r, s, "xray_version")
		}
	}
	return nil
}

func secretString(s string) secret.String { return secret.String(s) }
