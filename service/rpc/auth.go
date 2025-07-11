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
type authCache struct {
	mu       sync.RWMutex
	cache    map[string]*authCacheEntry
	maxSize  int
	ttl      time.Duration
}

type authCacheEntry struct {
	clientID   uint64
	expireTime time.Time
}

// newAuthCache 创建新的认证缓存
func newAuthCache(maxSize int, ttl time.Duration) *authCache {
	ac := &authCache{
		cache:   make(map[string]*authCacheEntry),
		maxSize: maxSize,
		ttl:     ttl,
	}
	
	// 启动定期清理过期条目的goroutine
	go ac.cleanupExpired()
	
	return ac
}

// get 从缓存获取认证结果
func (ac *authCache) get(clientSecret string) (uint64, bool) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	
	entry, exists := ac.cache[clientSecret]
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
	ac.mu.Lock()
	defer ac.mu.Unlock()
	
	// 如果缓存已满，删除最旧的条目
	if len(ac.cache) >= ac.maxSize {
		var oldestKey string
		var oldestTime time.Time
		
		for k, v := range ac.cache {
			if oldestTime.IsZero() || v.expireTime.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.expireTime
			}
		}
		
		if oldestKey != "" {
			delete(ac.cache, oldestKey)
		}
	}
	
	ac.cache[clientSecret] = &authCacheEntry{
		clientID:   clientID,
		expireTime: time.Now().Add(ac.ttl),
	}
}

// invalidate 使特定条目失效
func (ac *authCache) invalidate(clientSecret string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	
	delete(ac.cache, clientSecret)
}

// clear 清空整个缓存
func (ac *authCache) clear() {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	
	ac.cache = make(map[string]*authCacheEntry)
}

// cleanupExpired 定期清理过期条目
func (ac *authCache) cleanupExpired() {
	ticker := time.NewTicker(ac.ttl / 2)
	defer ticker.Stop()
	
	for range ticker.C {
		ac.mu.Lock()
		now := time.Now()
		
		for k, v := range ac.cache {
			if now.After(v.expireTime) {
				delete(ac.cache, k)
			}
		}
		
		ac.mu.Unlock()
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

	// 先尝试从缓存获取
	if clientID, found := globalAuthCache.get(clientSecret); found {
		return clientID, nil
	}

	// 缓存未命中，进行实际认证
	singleton.ServerLock.RLock()
	clientID, hasID := singleton.SecretToID[clientSecret]
	if !hasID {
		singleton.ServerLock.RUnlock()
		return 0, status.Errorf(codes.Unauthenticated, "客户端认证失败")
	}
	_, hasServer := singleton.ServerList[clientID]
	singleton.ServerLock.RUnlock()
	
	if !hasServer {
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