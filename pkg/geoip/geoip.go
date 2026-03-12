package geoip

import (
	"embed"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	maxminddb "github.com/oschwald/maxminddb-golang"
)

//go:embed geoip.db
var geoDBFS embed.FS

// IPInfo 用于 IPInfo 格式的 MMDB（扁平结构）
type IPInfo struct {
	Country       string `maxminddb:"country_code"`   // 国家代码，如 US、CN
	CountryName   string `maxminddb:"country"`        // 国家名称，如 United States、China
	Continent     string `maxminddb:"continent_code"` // 洲代码，如 NA、AS
	ContinentName string `maxminddb:"continent"`      // 洲名称，如 North America、Asia
}

// maxmindRecord 用于 MaxMind GeoLite2-Country 格式的 MMDB（嵌套结构）
type maxmindRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Continent struct {
		Code  string            `maxminddb:"code"`
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"continent"`
}

// dbFormat 标识当前加载的 MMDB 数据库格式
type dbFormat int

const (
	formatIPInfo  dbFormat = iota // IPInfo 扁平格式
	formatMaxMind                 // MaxMind GeoLite2 嵌套格式
)

var (
	db       *maxminddb.Reader
	dbMu     sync.RWMutex
	dbOnce   sync.Once // 向后兼容：保证至少初始化一次
	dbPath   string    // 当前加载的数据库路径
	dbFmt    dbFormat  // 当前数据库格式
	initDone bool      // 标记是否已通过 Init() 显式初始化
	confPath string    // 记录 Init 传入的路径，供 ensureInit 使用
)

// Init 初始化 GeoIP 数据库
// 优先加载 path 指定的外部文件；path 为空或加载失败时尝试内嵌数据库
func Init(path string) error {
	dbMu.Lock()
	defer dbMu.Unlock()

	confPath = path
	return initLocked(path)
}

// initLocked 在已持有 dbMu 写锁时执行初始化（内部方法）
func initLocked(path string) error {
	// 关闭旧的数据库
	if db != nil {
		db.Close()
		db = nil
		dbPath = ""
	}

	// 尝试从外部 MMDB 文件加载
	if path != "" {
		absPath := resolvePath(path)
		log.Printf("NG>> GeoIP: 尝试加载外部数据库: %s (原始路径: %s)", absPath, path)

		reader, err := loadFromFile(absPath)
		if err != nil {
			log.Printf("NG>> GeoIP: 外部数据库加载失败: %v", err)
			// 外部加载失败，继续尝试内嵌
		} else {
			db = reader
			dbPath = absPath
			dbFmt = detectFormat(reader)
			initDone = true
			log.Printf("NG>> GeoIP: 已加载外部数据库: %s (类型: %s, 格式: %s, 记录数: %d)",
				absPath, reader.Metadata.DatabaseType, formatName(dbFmt),
				reader.Metadata.NodeCount)
			return nil
		}
	}

	// 尝试内嵌数据库
	reader, err := loadEmbedded()
	if err != nil {
		// 内嵌数据库也不可用（可能是占位文件）
		if path != "" {
			return fmt.Errorf("外部数据库 %s 加载失败，内嵌数据库也不可用: %w", path, err)
		}
		log.Printf("NG>> GeoIP: 内嵌数据库不可用（%v），需要在配置中指定 geoipdb 路径", err)
		initDone = true // 标记已尝试初始化，避免重复尝试
		return nil      // 不返回 error，允许程序继续运行
	}

	db = reader
	dbPath = ""
	dbFmt = detectFormat(reader)
	initDone = true
	log.Printf("NG>> GeoIP: 已加载内嵌数据库 (格式: %s, 记录数: %d)",
		formatName(dbFmt), reader.Metadata.NodeCount)
	return nil
}

// Reload 重新加载 GeoIP 数据库（用于配置热更新）
func Reload(path string) error {
	return Init(path)
}

// ensureInit 确保 GeoIP 数据库至少被初始化一次（向后兼容）
// 如果 Init() 从未被显式调用，则使用内嵌数据库进行初始化
func ensureInit() {
	if initDone {
		return
	}
	dbOnce.Do(func() {
		if initDone {
			return
		}
		log.Printf("NG>> GeoIP: Lookup 时发现未初始化，自动加载内嵌数据库")
		_ = initLocked("")
	})
}

// resolvePath 将相对路径解析为绝对路径（相对于当前工作目录）
func resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// detectFormat 通过数据库元数据自动检测 MMDB 格式
func detectFormat(reader *maxminddb.Reader) dbFormat {
	dbType := reader.Metadata.DatabaseType
	if strings.Contains(dbType, "GeoLite2") || strings.Contains(dbType, "GeoIP2") {
		return formatMaxMind
	}
	return formatIPInfo
}

// formatName 返回格式的可读名称
func formatName(f dbFormat) string {
	switch f {
	case formatMaxMind:
		return "MaxMind"
	default:
		return "IPInfo"
	}
}

// loadFromFile 从外部 MMDB 文件加载数据库
func loadFromFile(path string) (*maxminddb.Reader, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("文件不存在: %s", path)
		}
		return nil, fmt.Errorf("文件不可访问: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("路径是目录而非文件: %s", path)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("文件为空: %s", path)
	}
	if info.Size() < 128 {
		return nil, fmt.Errorf("文件过小 (%d 字节)，不是有效的 MMDB: %s", info.Size(), path)
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("MMDB 解析失败 (文件大小: %d 字节): %w", info.Size(), err)
	}
	return reader, nil
}

// loadEmbedded 从内嵌资源加载数据库
func loadEmbedded() (*maxminddb.Reader, error) {
	data, err := geoDBFS.ReadFile("geoip.db")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌数据失败: %w", err)
	}
	if len(data) < 128 {
		return nil, fmt.Errorf("内嵌数据库为占位文件 (%d 字节)，非有效 MMDB", len(data))
	}
	reader, err := maxminddb.FromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("内嵌数据库解析失败: %w", err)
	}
	return reader, nil
}

// GetDBPath 返回当前加载的数据库路径，空字符串表示使用内嵌数据库
func GetDBPath() string {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return dbPath
}

// IsAvailable 返回 GeoIP 数据库是否可用
func IsAvailable() bool {
	dbMu.RLock()
	defer dbMu.RUnlock()
	if !initDone {
		return false
	}
	return db != nil
}

// Lookup 查询 IP 对应的国家/地区代码
func Lookup(ip net.IP, record *IPInfo) (string, error) {
	dbMu.RLock()
	// 向后兼容：如果 Init() 从未被调用，自动加载内嵌数据库
	if !initDone {
		dbMu.RUnlock()
		dbMu.Lock()
		ensureInit()
		dbMu.Unlock()
		dbMu.RLock()
	}
	defer dbMu.RUnlock()

	if db == nil {
		return "", fmt.Errorf("geoip database not available")
	}

	switch dbFmt {
	case formatMaxMind:
		return lookupMaxMind(ip, record)
	default:
		return lookupIPInfo(ip, record)
	}
}

// lookupIPInfo 使用 IPInfo 扁平格式查询
func lookupIPInfo(ip net.IP, record *IPInfo) (string, error) {
	err := db.Lookup(ip, record)
	if err != nil {
		return "", err
	}
	if record.Country != "" {
		return strings.ToLower(record.Country), nil
	}
	if record.Continent != "" {
		return strings.ToLower(record.Continent), nil
	}
	return "", fmt.Errorf("IP not found")
}

// lookupMaxMind 使用 MaxMind 嵌套格式查询，并填充 IPInfo 结构
func lookupMaxMind(ip net.IP, record *IPInfo) (string, error) {
	var mmRecord maxmindRecord
	err := db.Lookup(ip, &mmRecord)
	if err != nil {
		return "", err
	}

	record.Country = mmRecord.Country.ISOCode
	record.CountryName = mmRecord.Country.Names["en"]
	record.Continent = mmRecord.Continent.Code
	record.ContinentName = mmRecord.Continent.Names["en"]

	if record.Country != "" {
		return strings.ToLower(record.Country), nil
	}
	if record.Continent != "" {
		return strings.ToLower(record.Continent), nil
	}
	return "", fmt.Errorf("IP not found")
}
