package storage

import (
	"backend/pkg/db"
	"sync"
)

// StorageAdapter 存储适配器，统一管理所有存储模块
type StorageAdapter struct {
	dbManager          *db.DBManager
	positionLogic      *PositionLogicStorage
	tradeHistory       *TradeStorage
	cycleSnapshot      *CycleSnapshotStorage
	decisionLogs       *DecisionStorage
	cache              *CacheStorage
	initOnce           sync.Once
	initErr            error
}

// NewStorageAdapter 创建存储适配器
func NewStorageAdapter(dbDir string) (*StorageAdapter, error) {
	dbManager, err := db.NewDBManager(dbDir)
	if err != nil {
		return nil, err
	}

	adapter := &StorageAdapter{
		dbManager: dbManager,
	}

	// 延迟初始化各个存储模块
	adapter.initOnce.Do(func() {
		adapter.initErr = adapter.initStorages()
	})

	if adapter.initErr != nil {
		return nil, adapter.initErr
	}

	return adapter, nil
}

// initStorages 初始化所有存储模块
func (sa *StorageAdapter) initStorages() error {
	// 初始化持仓逻辑存储
	positionLogic, err := NewPositionLogicStorage(sa.dbManager)
	if err != nil {
		return err
	}
	sa.positionLogic = positionLogic

	// 初始化交易记录存储
	tradeHistory, err := NewTradeStorage(sa.dbManager)
	if err != nil {
		return err
	}
	sa.tradeHistory = tradeHistory

	// 初始化周期快照存储
	cycleSnapshot, err := NewCycleSnapshotStorage(sa.dbManager)
	if err != nil {
		return err
	}
	sa.cycleSnapshot = cycleSnapshot

	// 初始化决策记录存储
	decisionLogs, err := NewDecisionStorage(sa.dbManager)
	if err != nil {
		return err
	}
	sa.decisionLogs = decisionLogs

	// 初始化缓存存储
	cache, err := NewCacheStorage(sa.dbManager)
	if err != nil {
		return err
	}
	sa.cache = cache

	return nil
}

// GetPositionLogicStorage 获取持仓逻辑存储
func (sa *StorageAdapter) GetPositionLogicStorage() *PositionLogicStorage {
	return sa.positionLogic
}

// GetTradeStorage 获取交易记录存储
func (sa *StorageAdapter) GetTradeStorage() *TradeStorage {
	return sa.tradeHistory
}

// GetCycleSnapshotStorage 获取周期快照存储
func (sa *StorageAdapter) GetCycleSnapshotStorage() *CycleSnapshotStorage {
	return sa.cycleSnapshot
}

// GetDecisionStorage 获取决策记录存储
func (sa *StorageAdapter) GetDecisionStorage() *DecisionStorage {
	return sa.decisionLogs
}

// GetCacheStorage 获取缓存存储
func (sa *StorageAdapter) GetCacheStorage() *CacheStorage {
	return sa.cache
}

// Close 关闭所有存储连接
func (sa *StorageAdapter) Close() error {
	return sa.dbManager.Close()
}

