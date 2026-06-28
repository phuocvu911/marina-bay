Naming — the registry map (minor → name) plus assetName(). Edit the map to rename assets; no re-flashing.

Fleet filter — only iBeacons matching your fleetUUID are tracked, so ambient iBeacons from phones or other people's beacons in the marina don't pollute your data. (Right now it's the i3 factory UUID; when you give your beacons a custom UUID in BeaconSET+, change this one constant.)

Last-seen store — a sync.RWMutex-protected map[uint16]*assetState holding the most recent sighting per asset: RSSI, which gateway heard it, and the timestamp. Writes take Lock, the snapshot read takes RLock — your familiar concurrency pattern, and it matters here because the HTTP server handles ingest and queries on different goroutines.
