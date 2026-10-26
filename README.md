# microServer

A service that buys pay-as-you-go servers through provider APIs, checks whether their IP is reachable from inside Iran, rotates the IP if it is filtered, installs 3x-ui, applies a shared template (inbound/outbound/routing) to every panel, and finally uploads the configs to **vpn-pannel**.

- **Backend:** Go + Fiber + GORM + SQLite (CGO-free), MVC layout:
  - `internal/models` (M)
  - `internal/controllers` (C)
  - `web/` (V, React)
- **Dashboard:** React 19 + antd 6, RTL/Persian, dark/light, responsive, installable as a PWA. The build is embedded in the binary.
- **Telegram bot:** `/servers`, `/status`, `/new`, `/checkall`, `/sync`, with "Rotate IP", "Check", "Sync", "Links", "Upload" and "Delete" buttons.

## Providers

| Provider | How the IP changes | Keep the old IP |
|---|---|---|
| Hetzner Cloud | Primary IP swap (poweroff → unassign → new IP → poweron; the disk and 3x-ui stay) | ✔ |
| DigitalOcean | Reserved IP (no reboot) | ✔ |
| Vultr | Recreate (the old IP is converted to a Reserved IP) | ✔ |
| Linode | Recreate | ✘ |
| OVHcloud | Recreate | ✘ |

When a server is recreated, 3x-ui and the template are installed again automatically.

## Server creation flow (`provision` job)

1. Create the server with our SSH key (via the provider API and cloud-init).
   - If a kept IP has been out of the filter for long enough, it is reused.
2. Check reachability from Iran, using TCP to port 22 (nothing else is listening yet):
   - Sources: check-host (ir1..ir7) and Globalping (consumer networks such as MCI, TCI and Rightel), plus a control check from outside Iran.
   - Verdicts:
     - `healthy` when at least N Iranian probes across at least M different ASNs connect.
     - `filtered` when Iran fails but the outside check succeeds.
     - `down` when it fails from outside too.
   - If filtered, the IP is rotated and checked again, up to `rotate.max_attempts` times.
3. Over SSH, 3x-ui is installed non-interactively:
   - `XUI_NONINTERACTIVE=1 … install.sh v3.7.0`
   - Then the credentials and API token are read from `/etc/x-ui/install-result.env`.
   - The panel is bound to `127.0.0.1`. All API calls go through an SSH tunnel, so the panel is never exposed on the internet.
4. The shared template is applied:
   - Managed inbounds (`[ms] ` prefix) are created, updated or removed by port.
   - `outbounds`, `routing` and `dns` are replaced in the xray template.
   - xray is restarted.
5. The links are built and uploaded to vpn-pannel:
   - Upload: `POST /admin/v2ray/new` with `mode=link`.
   - On IP rotation: `replace-address` is called instead.
   - On server deletion: `bulk-active=false` is called instead.

Each step is saved in the `jobs` table. If the service restarts, the job resumes from the step it was on. The dashboard shows logs live over SSE.

## Running

```bash
cp .env.sample .env    # set ADMIN_PASSWORD
cd web && npm ci && npm run build && cd ..
go run ./cmd/server    # http://localhost:8090
```

Or with Docker: `docker compose up -d --build`

For frontend development, run `npm run dev` inside `web/`. It proxies `/api` to port 8090.

## Tests

```bash
go test ./...
```

What the tests cover:
- The exact call order of the Hetzner Primary IP swap.
- Parsing check-host and Globalping responses, and the verdict rules.
- Generating vless/vmess/trojan links, with Reality keys that stay stable.
- The vpn-pannel client, including re-login when the session expires.
- The full provision → filtered → rotate → install → apply → upload → rotate → replace-address flow, using a fake provider, SSH and panel.

## Settings worth knowing

| Key | Default | Description |
|---|---|---|
| `xui.version` | `v3.7.0` | 3x-ui version for new servers. Per server, it can be changed from the server page. |
| `xray.version` | — | Xray core version. Empty means whatever 3x-ui installs. |
| `check.min_ok` / `check.min_asn` | 2 / 2 | Healthy threshold |
| `check.interval_min` | 30 | Automatic re-check of every server |
| `rotate.auto` | false | Rotate the IP automatically when a server becomes filtered |
| `rotate.keep_filtered_ip` | false | Keep a filtered IP so it can be tried again later (this costs money) |
