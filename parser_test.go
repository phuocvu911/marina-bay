package main

import (
	"encoding/base64"
	"testing"
)

// Real advertising payload captured from a Minew i3.
const samplePayload = "AgEGGv9MAAIV4sVttd/7SNKwYND1pxCW4AAAAADF"

func TestParseIBeaconRealPayload(t *testing.T) {
	adv, err := base64.StdEncoding.DecodeString(samplePayload)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	b, ok := parseIBeacon(adv)
	if !ok {
		t.Fatal("expected an iBeacon frame, got none")
	}
	if b.UUID != "E2C56DB5-DFFB-48D2-B060-D0F5A71096E0" {
		t.Errorf("UUID = %s, want E2C56DB5-DFFB-48D2-B060-D0F5A71096E0", b.UUID)
	}
	if b.Major != 0 {
		t.Errorf("Major = %d, want 0", b.Major)
	}
	if b.Minor != 0 {
		t.Errorf("Minor = %d, want 0", b.Minor)
	}
	if b.TxPower != -59 {
		t.Errorf("TxPower = %d, want -59", b.TxPower)
	}
}

func TestParseIBeaconRejectsNonIBeacon(t *testing.T) {
	// Eddystone-URL frame: flags + service UUID 0xFEAA + service data.
	eddystone := []byte{
		0x02, 0x01, 0x06,
		0x03, 0x03, 0xAA, 0xFE,
		0x0D, 0x16, 0xAA, 0xFE, 0x10, 0xC5, 0x02, 'm', 'i', 'n', 'e', 'w', 0x08,
	}
	if _, ok := parseIBeacon(eddystone); ok {
		t.Error("Eddystone frame parsed as iBeacon")
	}
}

func TestParseIBeaconTruncatedPayload(t *testing.T) {
	adv, _ := base64.StdEncoding.DecodeString(samplePayload)
	for cut := 0; cut < len(adv); cut++ {
		if _, ok := parseIBeacon(adv[:cut]); ok {
			t.Errorf("truncated payload (%d bytes) should not parse", cut)
		}
	}
}
