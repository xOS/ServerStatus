package geoip

import (
	"embed"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

//go:embed geoip.db
var geoDBFS embed.FS

// IPInfo 表示 IP 地理位置信息
type IPInfo struct {
	Country       string `maxminddb:"country"`
	CountryName   string `maxminddb:"country_name"`
	Continent     string `maxminddb:"continent"`
	ContinentName string `maxminddb:"continent_name"`
}

var (
	db     *maxminddb.Reader
	dbMu   sync.RWMutex
	dbOnce sync.Once
	dbPath string // 当前加载的外部数据库路径
)

// Init 初始化 GeoIP 数据库
// 如果 path 不为空，优先从外部文件加载；否则使用内嵌数据库
func Init(path string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	// 关闭旧的数据库
	if db != nil {
		db.Close()
		db = nil
	}

	// 尝试从外部 MMDB 文件加载
	if path != "" {
		reader, err := loadFromFile(path)
		if err != nil {
			log.Printf("NG>> 无法加载外部 GeoIP 数据库 %s: %v，回退到内嵌数据库", path, err)
		} else {
			db = reader
			dbPath = path
			log.Printf("NG>> 已加载外部 GeoIP 数据库: %s", path)
			return nil
		}
	}

	// 回退到内嵌数据库
	reader, err := loadEmbedded()
	if err != nil {
		return fmt.Errorf("无法加载内嵌 GeoIP 数据库: %w", err)
	}
	db = reader
	dbPath = ""
	log.Printf("NG>> 已加载内嵌 GeoIP 数据库")
	return nil
}

// Reload 重新加载 GeoIP 数据库（用于配置热更新）
func Reload(path string) error {
	return Init(path)
}

// loadFromFile 从外部 MMDB 文件加载数据库
func loadFromFile(path string) (*maxminddb.Reader, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("文件不存在: %w", err)
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 MMDB 文件失败: %w", err)
	}
	return reader, nil
}

// loadEmbedded 从内嵌资源加载数据库
func loadEmbedded() (*maxminddb.Reader, error) {
	data, err := geoDBFS.ReadFile("geoip.db")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌数据失败: %w", err)
	}
	reader, err := maxminddb.FromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("解析内嵌数据失败: %w", err)
	}
	return reader, nil
}

// ensureInit 确保数据库已初始化（兼容未显式调用 Init 的场景）
func ensureInit() {
	dbOnce.Do(func() {
		dbMu.RLock()
		initialized := db != nil
		dbMu.RUnlock()
		if !initialized {
			if err := Init(""); err != nil {
				log.Printf("NG>> GeoIP 自动初始化失败: %v", err)
			}
		}
	})
}

// GetDBPath 返回当前加载的数据库路径，空字符串表示使用内嵌数据库
func GetDBPath() string {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return dbPath
}

// Lookup 查询 IP 对应的国家/地区代码
func Lookup(ip net.IP, record *IPInfo) (string, error) {
	ensureInit()

	dbMu.RLock()
	defer dbMu.RUnlock()

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
