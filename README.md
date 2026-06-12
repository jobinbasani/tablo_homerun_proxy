# Tablo HomeRun Proxy

Go implementation of a Tablo 4th Gen to HDHomeRun-compatible proxy for Plex Live TV.

## What it does

- Exposes HDHomeRun-style endpoints for Plex: `/discover.json`, `/lineup.json`, `/lineup_status.json`, and `/channel/:id`.
- Authenticates with Lighthouse/Tablo, stores encrypted credentials in `creds.bin`, and caches `lineup.json`.
- Requests live Tablo watch URLs on demand and pipes them through `ffmpeg` as MPEG-TS.
- Optionally builds an XMLTV guide at `/guide.xml`.

## Run locally

```bash
go run .
```

The first run creates `.env` with defaults. Set `USER_NAME` and `USER_PASS` for non-interactive setup, or leave them empty to be prompted.

## Docker

```bash
docker build -t tablo-homerun-proxy .
docker run -d \
  --name tablo-homerun-proxy \
  -p 8181:8181 \
  -v ./data:/data \
  -e USER_NAME="<tablo username>" \
  -e USER_PASS="<tablo password>" \
  tablo-homerun-proxy
```

After `creds.bin` exists in `./data`, remove `USER_NAME` and `USER_PASS` from the container config.

## Important flags

- `--creds`: recreate credentials.
- `--lineup`: force a lineup and guide refresh, then exit.
- `--outdir`: directory for `creds.bin`, `lineup.json`, `guide.xml`, schedules, and logs.
- `--xml`: enable XMLTV guide generation.
- `--ott`: include OTT channels.
- `--ip_address`: override the advertised IP address Plex should use.
