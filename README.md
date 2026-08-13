# HSK Asset Tracker

BLE asset tracking for Helsingfors Segelklubb. Assets carry Minew i3 iBeacons;
four Minew G1-E gateways POST what they hear to this server, which resolves
each asset to a **sector** of the floor (loudest-gateway-wins on smoothed
RSSI) and serves a phone-friendly search UI with a floor plan.

## Run

```sh
go run . -db ./marina.db              # listens on :8080
go run . -db ./marina.db -addr :9000  # or LISTEN_ADDR=:9000, or PORT=9000
go run . -db ./marina.db -verbose     # log every beacon sighting
go run . -db ""                       # no persistence, in-memory only
go test ./...
```

The database defaults to `/data/marina.db`, where the deployed volume mounts,
so a laptop needs `-db ./marina.db`. See [DEPLOY.md](DEPLOY.md) for deploying
to Fly.io.

Go 1.22+. One dependency: `modernc.org/sqlite`, the pure-Go SQLite driver — no
cgo, so the binary stays static and neither the build nor the runtime image
needs a C toolchain. No build step for the UI.

## Configure

Both registries are hardcoded in `registry.go` — edit and redeploy.

`zones` is the list of sectors, west to east, one gateway each:

```go
{Name: "West Wing", MAC: "ac233fc26fb0", X: 0.00, Y: 0, W: 0.14, H: 1, GwX: 0.07, GwY: 0.80}
```

- `MAC` — the gateway's MAC. Case and separators don't matter: the label on a
  G1-E prints `AC:23:3F:C2:6F:B0` while the heartbeat sends `ac233fc26fb0`, so
  both are folded to the same key.
- `X, Y, W, H` — the sector rectangle on the floor plan, normalised 0..1 from
  the top-left. This is what the map highlights.
- `GwX, GwY` — where the gateway is mounted, for the marker on the map.

`assets` maps a beacon minor number to an asset name. Beacon MACs are not
stable; identity is the minor only. All beacons share `fleetUUID` (currently
the i3 factory UUID — change the constant when you program a custom UUID in
BeaconSET+).

Point each G1-E at `POST http://<server>/ingest` (JSON mode). Editing either
registry means a redeploy — asset positions survive it, they are reloaded from
the database at startup.

## The floor plan

`templates/index.html` carries a hand-drawn SVG schematic of the floor —
shell, west-wing rooms, stairwells A and B, the IV-KH plant room, the escape
corridor and the open-plan desking — drawn in a `0 0 1000 250` viewBox so it
fills the map frame edge to edge.

To use a real plan instead, drop it at `static/floorplan.png` and swap the
`<svg class="plan">` element for `<img class="plan" src="/static/floorplan.png">`.
The sector overlay is positioned in percentages over the same frame, so
nothing else changes as long as the plan fills the image edge to edge.

Sectors are **full-depth vertical bands** on purpose. The floor is roughly 4:1
west to east, so RSSI separates one end from the other, but it cannot tell the
north desks from the south desks a few metres away. North/south resolution
would be precision the radio doesn't have.

## Endpoints

- `POST /ingest` — G1-E payloads. Always answers 200 (parse errors are logged)
  so the gateway keeps sending.
- `GET /api/assets[?q=substr]` — per asset: name, minor, sector, proximity
  hint, smoothed RSSI, seconds since last seen, online flag. `q` is a
  case-insensitive substring filter on the name.
- `GET /api/gateways` — each gateway's sector and last heartbeat, west to
  east (spot a dead gateway).
- `GET /` — the UI: live search, asset cards, and the floor plan with the
  sector holding each asset highlighted. Gateway markers dim when their
  heartbeat stops. Polls every 2 s.
- `GET /healthz` — plain 200 for the platform's health check.

## Zone resolution (tracker.go)

- **EMA smoothing** per (asset, gateway): `ema = 0.3·new + 0.7·old`. If a
  gateway's previous reading is older than the freshness window, the EMA
  restarts from the raw value instead of blending with stale signal.
- **Freshness**: only readings from the last 5 s compete for the sector.
- **Hysteresis**: a challenger gateway must beat the current sector's EMA by
  ≥ 4 dBm to take the asset — unless the current sector's reading has gone
  stale, in which case the loudest fresh gateway wins outright.
- **Staleness**: silent for 30 s → shown offline with its last sector and
  time; last-seen info is never deleted.
- **Proximity hint** from the winning EMA: ≥ −55 "very close", −55…−75
  "nearby", else "in the area".

Sectors are re-resolved on every sighting (~1 s per gateway), not on a timer,
so between sightings the last resolved sector stands until the 30 s staleness
cutoff.

## Persistence (store.go, retention.go)

The resolver above is the live path — SQLite is written on ingest and read
once at startup, never on a request. Two tables, deliberately different
lifetimes:

| Table | Contents | Lifetime |
|---|---|---|
| `sightings` | raw history: one row per resolved sighting | swept every 10 min, keeping `-retention` (default 1 h) |
| `asset_state` | one upserted row per asset: last zone, RSSI, time | **never swept** |

`sightings` grows at about a row per beacon per gateway per second and stops
being interesting an hour later, so it is swept on a timer and the row count
is logged each sweep. `asset_state` is the durable answer to "where did we
last see the trolley" — it has to outlive both a redeploy and an asset that
has been switched off in a shed since March, so nothing deletes it.

At startup `asset_state` is replayed into the resolver, so the UI shows last
known positions immediately rather than a fleet of never-seen assets. A
restart inside the 5 s freshness window resumes the contest under hysteresis;
after it the restored reading is stale and the first gateway to report wins
outright, which is the same rule any stale reading gets.

Writes happen after the tracker's lock is released, one transaction per
gateway batch. A failing disk is logged and `/ingest` still answers 200: zone
resolution has already happened in memory, and a 500 would only make the
gateway retry into the same error.

## Layout

| File | Concern |
|---|---|
| `registry.go` | fleet UUID, sector list (name, gateway, map geometry), minor→name map |
| `parser.go` | wire format structs, iBeacon AD-structure parser |
| `tracker.go` | in-memory state + sector resolver (mutex-guarded) |
| `store.go` | SQLite schema and the only SQL in the program |
| `retention.go` | the sweeper that keeps `sightings` bounded |
| `server.go` | HTTP handlers, embedded templates/static |
| `main.go` | flags and wiring |
| `Dockerfile`, `fly.toml` | deployment; see [DEPLOY.md](DEPLOY.md) |
