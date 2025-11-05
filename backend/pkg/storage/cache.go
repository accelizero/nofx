package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"backend/pkg/db"
	"time"
)

// CacheStorage 缓存存储（使用SQLite）
type CacheStorage struct {
	dbManager *db.DBManager
	db        *sql.DB
}

// NewCacheStorage 创建缓存存储
func NewCacheStorage(dbManager *db.DBManager) (*CacheStorage, error) {
	storage := &CacheStorage{
		dbManager: dbManager,
	}

	// 获取数据库连接
	database, err := dbManager.GetDB("cache")
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接失败: %w", err)
	}
	storage.db = database

	// 初始化表结构
	if err := storage.initTable(); err != nil {
		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}

	// 启动清理过期缓存的goroutine
	go storage.startCleanup()

	return storage, nil
}

// initTable 初始化表结构
func (s *CacheStorage) initTable() error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS cache (
		cache_key TEXT PRIMARY KEY,
		cache_data TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		expires_at DATETIME NOT NULL
	);
	
	CREATE INDEX IF NOT EXISTS idx_expires_at ON cache(expires_at);
	`

	_, err := s.db.Exec(createTableSQL)
	return err
}

// Get 获取缓存数据
func (s *CacheStorage) Get(key string) (interface{}, bool) {
	query := `
		SELECT cache_data, expires_at FROM cache
		WHERE cache_key = ?
	`

	var cacheData string
	var expiresAt time.Time

	err := s.db.QueryRow(query, key).Scan(&cacheData, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		log.Printf("⚠️  查询缓存失败 %s: %v", key, err)
		return nil, false
	}

	// 检查是否过期
	if time.Now().After(expiresAt) {
		// 删除过期缓存
		s.Delete(key)
		return nil, false
	}

	// 解析JSON数据
	var data interface{}
	if err := json.Unmarshal([]byte(cacheData), &data); err != nil {
		log.Printf("⚠️  解析缓存数据失败 %s: %v", key, err)
		return nil, false
	}

	return data, true
}

// Set 设置缓存数据
func (s *CacheStorage) Set(key string, data interface{}, ttl time.Duration) error {
	cacheData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("序列化缓存数据失败: %w", err)
	}

	expiresAt := time.Now().Add(ttl)

	query := `
		INSERT INTO cache (cache_key, cache_data, timestamp, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			cache_data = excluded.cache_data,
			timestamp = excluded.timestamp,
			expires_at = excluded.expires_at
	`

	_, err = s.db.Exec(query, key, string(cacheData), time.Now(), expiresAt)
	if err != nil {
		return fmt.Errorf("保存缓存失败: %w", err)
	}

	return nil
}

// Delete 删除缓存
func (s *CacheStorage) Delete(key string) error {
	query := `DELETE FROM cache WHERE cache_key = ?`

	_, err := s.db.Exec(query, key)
	if err != nil {
		return fmt.Errorf("删除缓存失败: %w", err)
	}

	return nil
}

// startCleanup 启动清理过期缓存的goroutine
func (s *CacheStorage) startCleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		}
	}
}

// cleanupExpired 清理过期缓存
func (s *CacheStorage) cleanupExpired() {
	query := `DELETE FROM cache WHERE expires_at < ?`

	result, err := s.db.Exec(query, time.Now())
	if err != nil {
		log.Printf("⚠️  清理过期缓存失败: %v", err)
		return
	}

	deleted, _ := result.RowsAffected()
	if deleted > 0 {
		log.Printf("🧹 清理过期缓存: %d 项", deleted)
	}
}

