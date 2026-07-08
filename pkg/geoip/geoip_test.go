package geoip

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupIPInfoLiteCountry(t *testing.T) {
	dbPath := filepath.Join("..", "..", "data", "ipinfo_lite.mmdb")
	if info, err := os.Stat(dbPath); err == nil && !info.IsDir() && info.Size() >= 128 {
		if err := Init(dbPath); err != nil {
			t.Fatalf("Init(%q) failed: %v", dbPath, err)
		}
	} else {
		if err := Init(""); err != nil {
			t.Fatalf("Init embedded GeoIP failed: %v", err)
		}
		if !IsAvailable() {
			t.Skipf("GeoIP database fixture is not available: %s", dbPath)
		}
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
