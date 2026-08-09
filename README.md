# HSK Marina Asset Tracker

BLE asset tracking for Helsingfors Segelklubb marina. Assets carry Minew i3
iBeacons; four Minew G1-E gateways POST what they hear to this server, which
resolves each asset to a **zone** (loudest-gateway-wins on smoothed RSSI) and
serves a phone-friendly search UI.

## Run

```sh
go run .                    # listens on :8080
go run . -addr :9000        # or LISTEN_ADDR=:9000
go run . -verbose           # log every beacon sighting
go test ./...
```

Go 1.22+, stdlib only — no dependencies, no build step.

## Configure

Both registries are hardcoded maps in `registry.go` — edit and redeploy:

- `gateways`: gateway MAC (lowercase, as sent in its heartbeat) → zone name.
  Two entries are still `REPLACE_ME_*` placeholders; fill them in with the
  real MACs of gateways 3–4.
- `assets`: beacon minor number → asset name. Beacon MACs are not stable;
  identity is the minor only. All beacons share `fleetUUID` (currently the i3
  factory UUID — change the constant when you program a custom UUID in
  BeaconSET+).

Point each G1-E at `POST http://<server>/ingest` (HTTP, JSON mode).

## Endpoints

- `POST /ingest` — G1-E payloads. Always answers 200 (parse errors are logged)
  so the gateway keeps sending.
- `GET /api/assets[?q=substr]` — per asset: name, minor, zone, proximity hint,
  smoothed RSSI, seconds since last seen, online flag. `q` is a
  case-insensitive substring filter on the name.
- `GET /api/gateways` — each gateway's zone and last heartbeat (spot a dead
  gateway).
- `GET /` — the UI: live search, asset cards, schematic zone map that
  highlights where the searched asset is. Polls every 2 s.

## Zone resolution (tracker.go)

- **EMA smoothing** per (asset, gateway): `ema = 0.3·new + 0.7·old`. If a
  gateway's previous reading is older than the freshness window, the EMA
  restarts from the raw value instead of blending with stale signal.
- **Freshness**: only readings from the last 5 s compete for the zone.
- **Hysteresis**: a challenger gateway must beat the current zone's EMA by
  ≥ 4 dBm to take the asset — unless the current zone's reading has gone
  stale, in which case the loudest fresh gateway wins outright.
- **Staleness**: silent for 30 s → shown offline with its last zone and time;
  last-seen info is never deleted.
- **Proximity hint** from the winning EMA: ≥ −55 "very close", −55…−75
  "nearby", else "in the area".

Zones are re-resolved on every sighting (~1 s per gateway), not on a timer, so
between sightings the last resolved zone stands until the 30 s staleness cutoff.

## Layout

| File | Concern |
|---|---|
| `registry.go` | fleet UUID, gateway→zone and minor→name maps |
| `parser.go` | wire format structs, iBeacon AD-structure parser |
| `tracker.go` | in-memory state + zone resolver (mutex-guarded) |
| `server.go` | HTTP handlers, embedded templates/static |
| `main.go` | flags and wiring |
