package tencentcloud

import (
	"errors"

	"github.com/libdns/libdns"
)

var (
	ErrRecordNotFound = errors.New("record not found")
	ErrNotValid       = errors.New("returned value is not valid")
)

type record struct {
	Type  string
	Name  string
	Value string
}

func (r record) libdnsRecord() (libdns.Record, error) {
	return libdns.RR{
		Type: r.Type,
		Name: r.Name,
		Data: r.Value,
	}.Parse()
}

func fromLibdnsRecord(r libdns.Record) record {
	rr := r.RR()

	host := rr.Name
	if host == "@" {
		host = ""
	}

	return record{
		Type:  rr.Type,
		Name:  host,
		Value: rr.Data,
	}
}
