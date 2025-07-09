package geoip

import (
	"embed"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

//go:embed geoip.db
var geoDBFS embed.FS

var (
	dbData []byte
	err    error
)

type IPInfo struct {
	Country       string `maxminddb:"country"`
	CountryName   string `maxminddb:"country_name"`
	Continent     string `maxminddb:"continent"`
	ContinentName string `maxminddb:"continent_name"`
}

var (
	db     *maxminddb.Reader
	dbOnce sync.Once
)

func init() {
	dbData, err = geoDBFS.ReadFile("geoip.db")
	if err != nil {
		log.Printf("NG>> Failed to open geoip database: %v", err)
	}
}

// initDB 初始化GeoIP数据库，只执行一次
func initDB() {
	if dbData != nil {
		var err error
		db, err = maxminddb.FromBytes(dbData)
		if err != nil {
			log.Printf("NG>> Failed to initialize geoip database: %v", err)
		}
	}
}

func Lookup(ip net.IP, record *IPInfo) (string, error) {
	// 确保数据库只初始化一次
	dbOnce.Do(initDB)

	if db == nil {
		return "", fmt.Errorf("geoip database not available")
	}

	err := db.Lookup(ip, record)
	if err != nil {
		return "", err
	}

	if record.Country != "" {
		return strings.ToLower(record.Country), nil
	} else if record.Continent != "" {
		return strings.ToLower(record.Continent), nil
	}

	return "", fmt.Errorf("IP not found")
}
