package rpc

import (
	"context"
	"sync"
	"time"

	"github.com/xos/serverstatus/service/singleton"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// authCache 用于缓存认证结果，避免频繁加锁
// 使用分片技术减少锁竞争
type authCache struct {
	shards   []*authCacheShard
	shardNum int
	maxSize  int
	ttl      time.Duration
}

type authCacheShard struct {
	mu    sync.RWMutex
	cache map[string]*authCacheEntry
}

type authCacheEntry struct {
	clientID   uint64
	expireTime time.Time
}

// newAuthCache 创建新的认证缓存
func newAuthCache(maxSize int, ttl time.Duration) *authCache {
	// 使用16个分片来减少锁竞争
	const shardNum = 16
	shards := make([]*authCacheShard, shardNum)

	for i := 0; i < shardNum; i++ {
		shards[i] = &authCacheShard{
			cache: make(map[string]*authCacheEntry),
		}
	}

	ac := &authCache{
		shards:   shards,
		shardNum: shardNum,
		maxSize:  maxSize,
		ttl:      ttl,
	}

	// 启动定期清理过期条目的goroutine
	go ac.cleanupExpired()

	return ac
}

// getShard 根据key计算分片索引
func (ac *authCache) getShard(key string) *authCacheShard {
	hash := uint32(0)
	for i := 0; i < len(key); i++ {
		hash = hash*31 + uint32(key[i])
	}
	return ac.shards[hash%uint32(ac.shardNum)]
}

// get 从缓存获取认证结果
func (ac *authCache) get(clientSecret string) (uint64, bool) {
	shard := ac.getShard(clientSecret)
	shard.mu.RLock()
	defer shard.mu.RUnlock()

	entry, exists := shard.cache[clientSecret]
	if !exists {
		return 0, false
	}

	// 检查是否过期
	if time.Now().After(entry.expireTime) {
		return 0, false
	}

	return entry.clientID, true
}

// set 设置缓存条目
func (ac *authCache) set(clientSecret string, clientID uint64) {
	shard := ac.getShard(clientSecret)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	// 如果当前分片的缓存已满，删除最旧的条目
	maxSizePerShard := ac.maxSize / ac.shardNum
	if maxSizePerShard < 10 {
		maxSizePerShard = 10 // 确保每个分片至少有10个条目
	}

	if len(shard.cache) >= maxSizePerShard {
		var oldestKey string
		var oldestTime time.Time

		for k, v := range shard.cache {
			if oldestTime.IsZero() || v.expireTime.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.expireTime
			}
		}

		if oldestKey != "" {
			delete(shard.cache, oldestKey)
		}
	}

	shard.cache[clientSecret] = &authCacheEntry{
		clientID:   clientID,
		expireTime: time.Now().Add(ac.ttl),
	}
}

// invalidate 使特定条目失效
func (ac *authCache) invalidate(clientSecret string) {
	shard := ac.getShard(clientSecret)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	delete(shard.cache, clientSecret)
}

// clear 清空整个缓存
func (ac *authCache) clear() {
	for _, shard := range ac.shards {
		shard.mu.Lock()
		shard.cache = make(map[string]*authCacheEntry)
		shard.mu.Unlock()
	}
}

// cleanupExpired 定期清理过期条目
func (ac *authCache) cleanupExpired() {
	ticker := time.NewTicker(ac.ttl / 2)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()

		// 并行清理所有分片，使用WaitGroup确保所有goroutine完成
		var wg sync.WaitGroup
		for _, shard := range ac.shards {
			wg.Add(1)
			go func(s *authCacheShard) {
				defer wg.Done()
				s.mu.Lock()
				for k, v := range s.cache {
					if now.After(v.expireTime) {
						delete(s.cache, k)
					}
				}
				s.mu.Unlock()
			}(shard)
		}
		wg.Wait() // 等待所有清理goroutine完成
	}
}

var (
	// 全局认证缓存实例
	globalAuthCache *authCache
)

func init() {
	// 初始化认证缓存：最多缓存10000个条目，TTL为5分钟
	globalAuthCache = newAuthCache(10000, 5*time.Minute)
}

type authHandler struct {
	ClientSecret string
}

func (a *authHandler) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{"client_secret": a.ClientSecret}, nil
}

func (a *authHandler) RequireTransportSecurity() bool {
	return false
}

func (a *authHandler) Check(ctx context.Context) (uint64, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return 0, status.Errorf(codes.Unauthenticated, "获取 metaData 失败")
	}

	var clientSecret string
	if value, ok := md["client_secret"]; ok {
		clientSecret = value[0]
	}

	if clientSecret == "" {
		return 0, status.Errorf(codes.Unauthenticated, "客户端认证失败")
	}

	// 先尝试从缓存获取
	if clientID, found := globalAuthCache.get(clientSecret); found {
		return clientID, nil
	}

	// 缓存未命中，进行实际认证
	singleton.ServerLock.RLock()
	clientID, hasID := singleton.SecretToID[clientSecret]
	hasServer := false
	if hasID {
		_, hasServer = singleton.ServerList[clientID]
	}
	singleton.ServerLock.RUnlock()

	if !hasID || !hasServer {
		return 0, status.Errorf(codes.Unauthenticated, "客户端认证失败")
	}

	// 认证成功，加入缓存
	globalAuthCache.set(clientSecret, clientID)

	return clientID, nil
}

// InvalidateAuthCache 使指定的认证缓存失效（当服务器被删除或密钥更改时调用）
func InvalidateAuthCache(clientSecret string) {
	if globalAuthCache != nil {
		globalAuthCache.invalidate(clientSecret)
	}
}

// ClearAuthCache 清空整个认证缓存（在重大配置变更时调用）
func ClearAuthCache() {
	if globalAuthCache != nil {
		globalAuthCache.clear()
	}
}
