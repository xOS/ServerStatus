package tencentcloud

import (
	"context"
	"errors"

	"github.com/libdns/libdns"
)

type Provider struct {
	SecretId  string
	SecretKey string
}

func (p *Provider) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	return p.listRecords(ctx, zone)
}

func (p *Provider) AppendRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	for _, record := range records {
		if err := p.createRecord(ctx, zone, record); err != nil {
			return nil, err
		}
	}

	return records, nil
}

func (p *Provider) SetRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	for _, record := range records {
		var id uint64
		if rid, err := p.findRecord(ctx, zone, record); err != nil {
			id = rid
			if errors.Is(err, ErrRecordNotFound) {
				if err := p.createRecord(ctx, zone, record); err != nil {
					return nil, err
				}
				continue
			}
		}
		if err := p.modifyRecord(ctx, id, zone, record); err != nil {
			return nil, err
		}
	}

	return records, nil
}

func (p *Provider) DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	for _, record := range records {
		if id, err := p.findRecord(ctx, zone, record); err != nil {
			if err := p.deleteRecord(ctx, id, zone, record); err != nil {
				return nil, err
			}
		}
	}

	return records, nil
}

// Interface guards
var (
	_ libdns.RecordGetter   = (*Provider)(nil)
	_ libdns.RecordAppender = (*Provider)(nil)
	_ libdns.RecordSetter   = (*Provider)(nil)
	_ libdns.RecordDeleter  = (*Provider)(nil)
)
