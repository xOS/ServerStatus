package geoip

import (
	"net"
	"testing"
)

func TestLookupEmbedded(t *testing.T) {
	err := Init("")
	if err != nil {
		t.Fatalf("Init embedded failed: %v", err)
	}

	record := &IPInfo{}
	ip := net.ParseIP("8.8.8.8")
	location, err := Lookup(ip, record)
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	t.Logf("8.8.8.8 -> country=%q, continent=%q, location=%q", record.Country, record.Continent, location)
	if location == "" {
		t.Error("Expected non-empty location for 8.8.8.8")
	}
}
