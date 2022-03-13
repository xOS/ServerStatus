package model

import "time"

const CtxKeyAuthorizedUser = "ckau"

const CacheKeyOauth2State = "p:a:state"

var Loc *time.Location

func init() {
	Loc = time.FixedZone("CST", 8*3600)
}

type Common struct {
	ID        uint64    `gorm:"primary_key"`
	CreatedAt time.Time `sql:"index"`
	UpdatedAt time.Time
	DeletedAt *time.Time `sql:"index"`
}

type Response struct {
	Code    int         `json:"code,omitempty"`
	Message string      `json:"message,omitempty"`
	Result  interface{} `json:"result,omitempty"`
}