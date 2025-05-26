package model

type Transfer struct {
	Common
	ServerID uint64 `gorm:"index" json:"server_id"`
	In       uint64 `json:"in"`
	Out      uint64 `json:"out"`
}