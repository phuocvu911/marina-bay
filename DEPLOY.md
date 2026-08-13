# Deploying to Fly.io

The tracker is one machine with one volume. Live zone decisions come out of
memory; the volume holds the SQLite database that makes "last seen" survive a
redeploy. Two machines would each answer from their own database and disagree
about where everything is, so scale this app by giving the machine more
memory, never by adding machines.

## One-time setup

```sh
fly auth login
fly apps create marina-bay          # then set the same name in fly.toml
fly volumes create marina_data --size 1 --region arn
```

The volume name (`marina_data`) and the region must match `[mounts].source`
and `primary_region` in `fly.toml`. One gigabyte is far more than the data
needs — the sightings table is swept hourly and `asset_state` is one small row
per asset — but it is the smallest volume Fly sells.

## Deploy

```sh
fly deploy                          # build the Dockerfile, release the machine
fly logs                            # watch it come up
fly status                          # machine state and health check
```

On boot the log should show the restore and then the listen line:

```
restored 3 assets from /data/marina.db
asset tracker listening on :8080 (POST /ingest, GET /, GET /api/assets, GET /healthz)
```

Then check it from outside:

```sh
curl https://marina-bay.fly.dev/healthz     # -> ok
curl https://marina-bay.fly.dev/api/gateways
```

## Pointing the gateways at it

In each G1-E's web UI, set the HTTP upload URL to:

```
https://marina-bay.fly.dev/ingest
```

JSON mode, POST, no authentication. Within a couple of seconds
`GET /api/gateways` should show a heartbeat for that gateway's MAC.

**If HTTPS upload fails**, do not start debugging the server. Point the
gateway at plain HTTP first:

```
http://marina-bay.fly.dev/ingest
```

If the plain-HTTP URL works and HTTPS does not, the problem is TLS on the
gateway — old G1-E firmware ships a stale CA bundle and cannot validate
current Let's Encrypt certificates. Confirm connectivity over HTTP, update the
gateway firmware, then move it back to HTTPS. If neither works, it is DNS or
the network the gateway is on, not the certificate.

This is why `force_https` is off in `fly.toml`: with it on, the plain-HTTP
test returns a 301 that the G1-E will not follow, which looks identical to the
server being down and hides the answer you were testing for.

## Configuration

Set in `[env]` in `fly.toml`, or as flags in the Dockerfile's entrypoint args.

| Setting | Default | What it does |
|---|---|---|
| `PORT` | `8080` | Listen port. Must match `internal_port` in `fly.toml`. |
| `LISTEN_ADDR` | — | Full listen address; wins over `PORT` if set. |
| `DB_PATH` / `-db` | `/data/marina.db` | Database file. `-db ""` runs with no persistence at all. |
| `-retention` | `1h` | How long raw sightings are kept before the sweeper deletes them. |
| `-verbose` | off | Log every beacon sighting. |

## Operating it

```sh
fly ssh console                                   # alpine shell on the machine
fly ssh console -C "ls -la /data"                 # volume contents
fly logs | grep "retention sweep"                 # sweeper is alive
```

The database is a plain SQLite file on the volume. To pull a copy down:

```sh
fly ssh sftp get /data/marina.db ./marina.db
```

Changing the gateway or asset registry means editing `registry.go` and
running `fly deploy` again — both registries are compiled in. Asset positions
survive that redeploy: they are reloaded from `asset_state` at startup.

## Running it locally

The default database path points at the volume, which does not exist on a
laptop, so give it a local one:

```sh
go run . -db ./marina.db            # http://localhost:8080
go run . -db ./marina.db -retention 10m -verbose
go run . -db ""                     # no persistence, previous behaviour
```
