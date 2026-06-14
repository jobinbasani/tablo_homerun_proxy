# Tablo HomeRun Proxy

Go implementation of a Tablo 4th Gen to HDHomeRun-compatible proxy for Plex Live TV.

## What it does

- Exposes HDHomeRun-style endpoints for Plex: `/discover.json`, `/lineup.json`, `/lineup_status.json`, and `/channel/:id`.
- Authenticates with Lighthouse/Tablo, stores encrypted credentials in SQLite, and caches `lineup.json`.
- Requests live Tablo watch URLs on demand and pipes them through `ffmpeg` as MPEG-TS.
- Optionally builds an XMLTV guide at `/guide.xml`.
- Includes an authenticated admin UI at `/admin` for setup, settings, status, actions, and logs.
- Stores runtime configuration in SQLite after first boot.

## Run locally

```bash
go run .
```

The first run creates `.env` with bootstrap defaults. Environment variables are loaded with `github.com/kelseyhightower/envconfig`, then runtime configuration is initialized in SQLite at `proxy.db`. Use `ADMIN_PASSWORD` to seed the admin UI password, or visit `/admin` and set it on first login.

## Docker

```bash
docker build -t tablo-homerun-proxy .
docker run -d \
  --name tablo-homerun-proxy \
  -p 8181:8181 \
  -v ./data:/data \
  -e ADMIN_PASSWORD="<admin password>" \
  tablo-homerun-proxy
```

Open `http://<host>:8181/admin` to log in, connect your Tablo account, select a device, and manage settings.

## Important flags

- `--creds`: recreate credentials.
- `--lineup`: force a lineup and guide refresh, then exit.
- `--outdir`: directory for `lineup.json`, `guide.xml`, schedules, and logs.
- `--db`: SQLite database path. Defaults to `<outdir>/proxy.db`.
- `--admin_password`: first-run admin password seed.
- `--xml`: enable XMLTV guide generation.
- `--ott`: include OTT channels.
- `--ip_address`: override the advertised IP address Plex should use.
