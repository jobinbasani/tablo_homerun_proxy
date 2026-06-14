# Tablo HomeRun Proxy

Go implementation of a Tablo 4th Gen to HDHomeRun-compatible proxy for Plex Live TV.

## What it does

- Exposes HDHomeRun-style endpoints for Plex: `/discover.json`, `/lineup.json`, `/lineup_status.json`, and `/channel/:id`.
- Advertises itself on the LAN with SSDP so Plex can discover it as an HDHomeRun-compatible tuner.
- Authenticates with Lighthouse/Tablo, stores encrypted credentials in SQLite, and caches `lineup.json`.
- Requests live Tablo watch URLs on demand and pipes them through `ffmpeg` as MPEG-TS.
- Optionally builds an XMLTV guide at `/guide.xml`.
- Includes an authenticated admin UI at `/admin` for setup, settings, status, and actions.
- Emits structured JSON logs to stdout/stderr for `docker logs` and log shippers.
- Stores runtime configuration in SQLite after first boot.

## Run locally

```bash
go run .
```

The first run creates `.env` with bootstrap defaults. Environment variables are loaded with `github.com/kelseyhightower/envconfig`, then runtime configuration is initialized in SQLite at `proxy.db`. Set `ADMIN_PASSWORD` to choose or rotate the admin UI password. If it is not set and no database password exists, the app generates a random admin password, prints it to the log, and saves it to the database. Existing database passwords are reused on restart.

## Docker

```bash
docker build -t tablo-homerun-proxy .
docker run -d \
  --name tablo-homerun-proxy \
  --network host \
  -v ./data:/data \
  -e ADMIN_PASSWORD="<admin password>" \
  tablo-homerun-proxy
```

Open `http://<host>:8181/admin` to log in, connect your Tablo account, select a device, and manage settings.

For Plex auto-discovery in Docker, use host networking as shown in `docker-compose.host.example.yml`. The normal bridge compose file still works for manual setup, but multicast SSDP discovery is generally not forwarded through Docker port publishing.

## Important flags

- `--creds`: recreate credentials.
- `--lineup`: force a lineup and guide refresh, then exit.
- `--outdir`: directory for `lineup.json`, `guide.xml`, and schedules.
- `--db`: SQLite database path. Defaults to `<outdir>/proxy.db`.
- `--admin_password`: admin password to save at startup. If omitted, the database password is reused, or a random password is logged and saved on first run.
- `--xml`: enable XMLTV guide generation.
- `--ott`: include OTT channels.
- `--ip_address`: override the advertised IP address Plex should use.
- `--level`: structured console log threshold. Defaults to `info`; use `error` to suppress non-critical runtime logs.
