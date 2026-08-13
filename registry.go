package main

import "strings"

// Registries for the MVP. Hardcoded on purpose — edit and redeploy.
// The beacon only carries a number; names, sectors and map geometry live here.

// fleetUUID: only iBeacons with this UUID are tracked, so ambient iBeacons
// from phones or other people's beacons in the building can't pollute the
// data. Right now this is the Minew i3 factory UUID; when you give the
// beacons a custom UUID in BeaconSET+, change this one constant.
const fleetUUID = "E2C56DB5-DFFB-48D2-B060-D0F5A71096E0"

// Zone is one sector of the floor — the area a single gateway owns.
//
// Geometry is normalised 0..1 over the floor plan with the origin at the
// top-left, so the map renders at any size and the hand-drawn schematic can
// be swapped for a real floor-plan image without touching these numbers.
//
// Sectors are full-depth vertical bands on purpose. The floor is roughly 4:1
// west to east, so RSSI can separate one end from the other, but it cannot
// tell the north desks from the south desks a few metres away. Claiming
// north/south resolution would be inventing precision the radio doesn't have.
type Zone struct {
	Name string `json:"name"`
	MAC  string `json:"mac"` // gateway MAC, lowercase hex, as sent in the heartbeat

	// Sector rectangle on the floor plan.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`

	// Where the gateway itself is mounted, for the map marker. Placed on the
	// escape corridor, which is where the power and the clear ceiling run are.
	GwX float64 `json:"gw_x"`
	GwY float64 `json:"gw_y"`
}

// zones lists the floor west to east — one gateway per sector. Boundaries
// follow the architecture rather than equal quarters: the west end is cut up
// into small rooms, the east half is open-plan desking.
var zones = []Zone{
	{Name: "West Wing", MAC: "ac233fc26fb0", X: 0.00, Y: 0, W: 0.14, H: 1, GwX: 0.07, GwY: 0.80},
	{Name: "Stair A", MAC: "ac233fc270d4", X: 0.14, Y: 0, W: 0.16, H: 1, GwX: 0.22, GwY: 0.80},
	{Name: "Central Office", MAC: "ac233fc26fad", X: 0.30, Y: 0, W: 0.32, H: 1, GwX: 0.46, GwY: 0.80},
	{Name: "East Office", MAC: "ac233fc26fb8", X: 0.62, Y: 0, W: 0.38, H: 1, GwX: 0.80, GwY: 0.80},
}

// normalizeMAC folds a MAC to the form the gateway sends it in: lowercase
// hex, no separators. The MAC printed on a G1-E label is uppercase and
// colon-separated, so without this a correctly-typed registry entry would
// silently never match a heartbeat.
func normalizeMAC(mac string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r == ':' || r == '-' || r == '.' || r == ' ':
			return -1
		}
		return r
	}, mac)
}

// gatewayZone indexes zones by normalised gateway MAC. Built once at startup
// so the hot ingest path is a map hit rather than a scan. The stored MACs are
// normalised in place, so the API reports them exactly as they arrive.
var gatewayZone = func() map[string]*Zone {
	m := make(map[string]*Zone, len(zones))
	for i := range zones {
		zones[i].MAC = normalizeMAC(zones[i].MAC)
		m[zones[i].MAC] = &zones[i]
	}
	return m
}()

// zoneMACs indexes zones by name, the reverse of gatewayZone. Persisted state
// records the zone an asset was last resolved to, and restoring it into the
// resolver means turning that name back into the gateway that owns it.
// Normalises independently rather than leaning on gatewayZone having already
// rewritten zones in place: package-level initialisers run in declaration
// order here, but normalizeMAC is idempotent and this way the two indexes
// cannot drift if either declaration moves.
var zoneMACs = func() map[string]string {
	m := make(map[string]string, len(zones))
	for _, z := range zones {
		m[z.Name] = normalizeMAC(z.MAC)
	}
	return m
}()

func zoneMAC(name string) (string, bool) {
	mac, ok := zoneMACs[name]
	return mac, ok
}

// assets maps a beacon's minor number to the asset name.
var assets = map[uint16]string{
	1: "cradle",
	2: "trolley",
	3: "bucket",
}

func assetName(minor uint16) string {
	if name, ok := assets[minor]; ok {
		return name
	}
	return ""
}

func zoneName(gatewayMAC string) string {
	if z, ok := gatewayZone[gatewayMAC]; ok {
		return z.Name
	}
	return "unknown gateway " + gatewayMAC
}
