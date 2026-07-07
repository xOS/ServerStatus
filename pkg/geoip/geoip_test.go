package geoip

import (
	"net"
	"path/filepath"
	"testing"
)

func TestLookupIPInfoLiteCountry(t *testing.T) {
	dbPath := filepath.Join("..", "..", "data", "ipinfo_lite.mmdb")
	if err := Init(dbPath); err != nil {
		t.Fatalf("Init(%q) failed: %v", dbPath, err)
	}

	record := &IPInfo{}
	country, err := Lookup(net.ParseIP("1.1.1.1"), record)
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	if country != "au" {
		t.Fatalf("expected au, got %q (record=%+v)", country, record)
	}
}
