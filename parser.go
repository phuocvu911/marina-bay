package main

import (
	"encoding/binary"
	"fmt"
)

// The G1-E POSTs a JSON array. Entries are heterogeneous:
//   - gateway heartbeat: {"gateway","timestamp","seq"}
//   - beacon sighting:   {"mac","timestamp","rssi","raw"}
//
// A single struct covers both shapes; Gateway != "" marks a heartbeat,
// MAC != "" marks a sighting.
type rawEntry struct {
	Gateway   string `json:"gateway"`
	Seq       int    `json:"seq"`
	MAC       string `json:"mac"`
	RSSI      int    `json:"rssi"`
	Raw       string `json:"raw"` // base64 BLE advertising payload
	Timestamp int64  `json:"timestamp"`
}

type iBeacon struct {
	UUID    string
	Major   uint16
	Minor   uint16
	TxPower int8 // measured RSSI at 1m
}

// parseIBeacon walks the BLE advertising structures ([len][type][payload]…)
// and extracts the iBeacon frame if present. Eddystone and other frames the
// beacon also broadcasts simply won't match and return ok=false.
func parseIBeacon(adv []byte) (iBeacon, bool) {
	for i := 0; i < len(adv); {
		length := int(adv[i])
		if length == 0 || i+1+length > len(adv) {
			break
		}
		adType := adv[i+1]
		payload := adv[i+2 : i+1+length]

		// 0xFF = manufacturer-specific. iBeacon = Apple (0x004C little endian
		// on the wire) + subtype 0x02 + length 0x15.
		if adType == 0xFF && len(payload) >= 25 &&
			payload[0] == 0x4C && payload[1] == 0x00 &&
			payload[2] == 0x02 && payload[3] == 0x15 {
			return iBeacon{
				UUID:    formatUUID(payload[4:20]),
				Major:   binary.BigEndian.Uint16(payload[20:22]),
				Minor:   binary.BigEndian.Uint16(payload[22:24]),
				TxPower: int8(payload[24]),
			}, true
		}
		i += 1 + length
	}
	return iBeacon{}, false
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
