package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"backend/pkg/db"
	"time"
)

// CycleSnapshotStorage 周期快照存储（使用SQLite）
type CycleSnapshotStorage struct {
	dbManager *db.DBManager
	db        *sql.DB
}

// NewCycleSnapshotStorage 创建周期快照存储
func NewCycleSnapshotStorage(dbManager *db.DBManager) (*CycleSnapshotStorage, error) {
	storage := &CycleSnapshotStorage{
		dbManager: dbManager,
	}

	// 获取数据库连接
	database, err := dbManager.GetDB("cycle_snapshots")
	if err != nil {
		return nil, fmt.Errorf("获取数据库连接失败: %w", err)
	}
	storage.db = database

	// 初始化表结构
	if err := storage.initTable(); err != nil {
		return nil, fmt.Errorf("初始化表结构失败: %w", err)
	}

	return storage, nil
}

// initTable 初始化表结构
func (s *CycleSnapshotStorage) initTable() error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS cycle_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		trader_id TEXT NOT NULL,
		cycle_number INTEGER NOT NULL,
		timestamp DATETIME NOT NULL,
		scan_interval INTEGER NOT NULL,
		snapshot_data TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(trader_id, cycle_number)
	);
	
	CREATE INDEX IF NOT EXISTS idx_trader_cycle ON cycle_snapshots(trader_id, cycle_number);
	CREATE INDEX IF NOT EXISTS idx_timestamp ON cycle_snapshots(timestamp);
	`

	_, err := s.db.Exec(createTableSQL)
	return err
}

// CycleSnapshot 周期完整快照（使用JSON存储完整数据）
type CycleSnapshot struct {
	TraderID          string                     `json:"trader_id"`
	CycleNumber       int                        `json:"cycle_number"`
	Timestamp         time.Time                   `json:"timestamp"`
	ScanInterval      int                        `json:"scan_interval"`
	AccountState      interface{}                 `json:"account_state"`
	MarketEnvironment interface{}                `json:"market_environment"`
	PositionsSnapshot interface{}                `json:"positions_snapshot"`
	AIDecision        interface{}                 `json:"ai_decision"`
	ExecutionResult   interface{}                 `json:"execution_result"`
	FollowUpPerformance interface{}              `json:"follow_up_performance,omitempty"`
	SystemMetrics     interface{}                 `json:"system_metrics"`
}

// LogCycleSnapshot 记录周期快照
func (s *CycleSnapshotStorage) LogCycleSnapshot(snapshot *CycleSnapshot) error {
	// 将快照序列化为JSON
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("序列化周期快照失败: %w", err)
	}

	query := `
		INSERT INTO cycle_snapshots (trader_id, cycle_number, timestamp, scan_interval, snapshot_data)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(trader_id, cycle_number) DO UPDATE SET
			timestamp = excluded.timestamp,
			scan_interval = excluded.scan_interval,
			snapshot_data = excluded.snapshot_data
	`

	_, err = s.db.Exec(query,
		snapshot.TraderID,
		snapshot.CycleNumber,
		snapshot.Timestamp,
		snapshot.ScanInterval,
		string(snapshotJSON),
	)

	if err != nil {
		return fmt.Errorf("保存周期快照失败: %w", err)
	}

	return nil
}

// GetCycleSnapshots 获取周期快照列表（按时间排序）
func (s *CycleSnapshotStorage) GetCycleSnapshots(limit int) ([]*CycleSnapshot, error) {
	query := `
		SELECT snapshot_data FROM cycle_snapshots
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, fmt.Errorf("查询周期快照失败: %w", err)
	}
	defer rows.Close()

	var snapshots []*CycleSnapshot
	for rows.Next() {
		var snapshotJSON string
		if err := rows.Scan(&snapshotJSON); err != nil {
			log.Printf("⚠️  扫描周期快照失败: %v", err)
			continue
		}

		var snapshot CycleSnapshot
		if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
			log.Printf("⚠️  解析周期快照失败: %v", err)
			continue
		}

		snapshots = append(snapshots, &snapshot)
	}

	return snapshots, rows.Err()
}

// GetCycleSnapshotByCycleNumber 根据周期编号获取快照
func (s *CycleSnapshotStorage) GetCycleSnapshotByCycleNumber(traderID string, cycleNum int) (*CycleSnapshot, error) {
	query := `
		SELECT snapshot_data FROM cycle_snapshots
		WHERE trader_id = ? AND cycle_number = ?
	`

	var snapshotJSON string
	err := s.db.QueryRow(query, traderID, cycleNum).Scan(&snapshotJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("未找到周期 %d 的快照", cycleNum)
	}
	if err != nil {
		return nil, fmt.Errorf("查询周期快照失败: %w", err)
	}

	var snapshot CycleSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return nil, fmt.Errorf("解析周期快照失败: %w", err)
	}

	return &snapshot, nil
}

