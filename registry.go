package main

// Registries for the MVP. Hardcoded on purpose — edit and redeploy.
// The beacon only carries a number; names and zones live here in software.

// fleetUUID: only iBeacons with this UUID are tracked, so ambient iBeacons
// from phones or other people's beacons in the marina can't pollute the data.
// Right now this is the Minew i3 factory UUID; when you give the beacons a
// custom UUID in BeaconSET+, change this one constant.
const fleetUUID = "E2C56DB5-DFFB-48D2-B060-D0F5A71096E0"

// gateways maps a gateway MAC (lowercase hex, exactly as sent in the
// heartbeat entry) to the zone it covers.
var gateways = map[string]string{
	"ac233fc26fb0": "Living Room", // real MAC of gateway 1
	"ac233fc270d4": "Kitchen",
	"REPLACE_ME_3": "Gas Station",
	"REPLACE_ME_4": "North Yard",
}

// assets maps a beacon's minor number to the asset name.
var assets = map[uint16]string{
	1: "craddle",
	2: "trolley",
}

func assetName(minor uint16) string {
	if name, ok := assets[minor]; ok {
		return name
	}
	return ""
}

func zoneName(gatewayMAC string) string {
	if zone, ok := gateways[gatewayMAC]; ok {
		return zone
	}
	return "unknown gateway " + gatewayMAC
}
