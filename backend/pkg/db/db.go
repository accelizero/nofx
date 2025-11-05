package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// DBManager 数据库管理器，管理多个SQLite数据库连接
type DBManager struct {
	databases map[string]*sql.DB
	mu        sync.RWMutex
	dbDir     string
}

// NewDBManager 创建数据库管理器
func NewDBManager(dbDir string) (*DBManager, error) {
	if dbDir == "" {
		dbDir = "data"
	}

	// 确保数据库目录存在
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	return &DBManager{
		databases: make(map[string]*sql.DB),
		dbDir:     dbDir,
	}, nil
}

// GetDB 获取或创建指定的数据库连接
// dbName: 数据库名称（不含扩展名），例如 "position_logic", "trade_history", "cache"
func (dm *DBManager) GetDB(dbName string) (*sql.DB, error) {
	dm.mu.RLock()
	db, exists := dm.databases[dbName]
	dm.mu.RUnlock()

	if exists {
		return db, nil
	}

	// 创建新的数据库连接
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// 双重检查
	if db, exists := dm.databases[dbName]; exists {
		return db, nil
	}

	// 构建数据库文件路径
	dbPath := filepath.Join(dm.dbDir, dbName+".db")

	// 打开数据库连接
	connStr := fmt.Sprintf("file:%s?cache=shared&mode=rwc", dbPath)
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s 失败: %w", dbName, err)
	}

	// 设置连接池参数
	db.SetMaxOpenConns(1) // SQLite建议每个数据库文件只使用一个连接
	db.SetMaxIdleConns(1)

	// 测试连接
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库连接测试失败 %s: %w", dbName, err)
	}

	// 启用外键约束
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("启用外键约束失败 %s: %w", dbName, err)
	}

	dm.databases[dbName] = db
	log.Printf("✓ 数据库连接已创建: %s", dbPath)

	return db, nil
}

// Close 关闭所有数据库连接
func (dm *DBManager) Close() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	var firstErr error
	for name, db := range dm.databases {
		if err := db.Close(); err != nil {
			log.Printf("⚠️  关闭数据库 %s 失败: %v", name, err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			log.Printf("✓ 数据库连接已关闭: %s", name)
		}
	}

	dm.databases = make(map[string]*sql.DB)
	return firstErr
}

// GetDBPath 获取数据库文件路径（用于备份等操作）
func (dm *DBManager) GetDBPath(dbName string) string {
	return filepath.Join(dm.dbDir, dbName+".db")
}

