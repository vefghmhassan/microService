# Cloudflare DNS + wildcard SSL — progress notes

Status: **in progress** (backend done and tested; frontend partly done; not tested against a real Cloudflare account yet).

## What it does
- **Cloudflare account:** add an API token (Zone:Read + DNS:Edit) on the "دامنه‌ها (کلودفلر)" page. The token is encrypted, like provider credentials.
- **Location:** a Location can optionally pick a Cloudflare account, a zone (domain), a subdomain pattern such as `fr{n}`, a Proxied switch and an SSL switch.
- **Hostnames:** every server built in that Location gets the lowest free number (`fr1.example.com`, `fr2…`). Links and vpn-pannel uploads use the hostname instead of the IP.
- **Replacement in the same zone and pattern:** it takes over the old server's hostname. The A record moves to the new IP, and vpn-pannel needs no replace-address because the links stay the same.
- **Replacement in another location:** it gets that location's name, and replace-address runs `fr1.x → de1.x`.
- **IP rotation:** updates the A record. Delete removes the record, unless another live server holds the name.
- **SSL:** one wildcard certificate per zone (`*.zone` + `zone`) from Let's Encrypt over DNS-01, using `golang.org/x/crypto/acme` (no lego dependency).
  - It is issued centrally and copied over SSH to `/root/cert/fullchain.pem` and `/root/cert/privkey.pem`.
  - The 3x-ui panel switches to https (`x-ui cert -webCert … -webCertKey …`). If the panel does not come up, it falls back to http.
  - A ticker renews certificates every 12h when fewer than 30 days remain, and pushes them to the servers.
  - The `acme.staging` setting switches to the staging CA for testing.
- **TLS links:** when a TLS inbound has no `serverName`, the link's `sni` is the hostname.
- **Locations without Cloudflare:** behave exactly as before.

## Files
- **Backend:**
  - `internal/services/cloudflare/` (API client + test)
  - `internal/services/certs/` (ACME)
  - `internal/services/orchestrator/dns.go` (assignHostname, syncDNS, removeDNS, pushCert, certPush/dnsSync jobs, RenewCerts) + `dns_test.go`
  - hooks in `flows.go` (rotate `dns` step, moveDestination by LinkHost, install → assignHostname, installAndApply → pushCert, delete → removeDNS) and `auto.go` (DNS copied onto candidates, finishInstall → syncDNS)
  - `internal/controllers/cloudflare.go`, the location fields in `controllers/locations.go`, routes in `internal/router.go`
  - models `DNS` (embedded in Location and Server), `CloudflareAccount`, `Certificate`, plus the Server fields `hostname`, `dns_record_id`, `dns_error`, `cert_not_after`, `location_id`
  - `cmd/server/main.go`: certs manager + `scheduleCerts`
- **API:**
  - `GET/POST /cloudflare`, `PUT/DELETE /cloudflare/:id`
  - `POST /cloudflare/:id/test`, `GET /cloudflare/:id/zones`
  - `POST /cloudflare/:id/certificate`, `GET /cloudflare/certificates`
  - `POST /servers/:id/cert` (push the certificate again), `POST /servers/:id/dns` (fix the record)
- **Frontend (done):** `web/src/pages/Cloudflare.tsx` (accounts, zones, certificate status and an issue button), a sidebar item and route, the Location form section and table column, and the types and hooks. `tsc -b` passes.

## Done since the first commit
- Server UI: `components/HostBadge.tsx` (hostname + SSL lock + DNS error) in the Servers table, ServerCard and ServerDetail; search matches the hostname; ServerActions menu has "به‌روزرسانی رکورد DNS" (`dns`) and "کپی مجدد گواهی SSL" (`cert`).
- Settings: `acme.staging` switch.
- `tsc -b` and `npm run build` pass. The Cloudflare page renders (checked on a throwaway instance on port 8091 with an empty data dir); the Location modal and server views were not clicked through yet.

## Remaining (TODO)
1. **Click through** the Location modal's Cloudflare section and the server views with real data.
4. **E2E with the real account. Agree on the zone and subdomain first, because this changes live DNS:**
   - Add the token, then check that the zones load.
   - Create a Location with `fr{n}` and SSL on, then auto-build a server.
   - Check the A record in Cloudflare, then run `curl -v https://fr1.<zone>:<panel port>`.
   - Replace the server, then check that the record moved and that the vpn-pannel links are unchanged.
5. **Check `/usr/local/x-ui/x-ui cert -webCert` on 3x-ui v3.7.0.** If the subcommand differs, adjust `pushCert` in `dns.go`.
6. **Note for TLS inbound templates:** point them at `/root/cert/fullchain.pem` and `/root/cert/privkey.pem`. They only work on servers that have SSL on.
7. **Later phase:** domain fronting. It builds on Proxied plus CDN-compatible inbounds (ws/grpc/xhttp on Cloudflare ports).
