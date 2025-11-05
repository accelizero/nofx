package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"backend/pkg/db"
	"time"
)

// PositionLogicStorage 持仓逻辑存储（使用SQLite）
type PositionLogicStorage struct {
	dbManager *db.DBManager
	db        *sql.DB
}

// NewPositionLogicStorage 创建持仓逻辑存储
func NewPositionLogicStorage(dbManager *db.DBManager) (*PositionLogicStorage, error) {
	storage := &PositionLogicStorage{
		dbManager: dbManager,
	}

	// 获取数据库连接
	database, err := dbManager.GetDB("position_logic")
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
func (s *PositionLogicStorage) initTable() error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS position_logic (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		symbol TEXT NOT NULL,
		side TEXT NOT NULL,
		entry_logic TEXT,
		exit_logic TEXT,
		stop_loss REAL DEFAULT 0,
		take_profit REAL DEFAULT 0,
		first_seen_time INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(symbol, side)
	);
	
	CREATE INDEX IF NOT EXISTS idx_symbol_side ON position_logic(symbol, side);
	`

	_, err := s.db.Exec(createTableSQL)
	return err
}

// PositionLogic 持仓逻辑结构
type PositionLogic struct {
	EntryLogic    *EntryLogic `json:"entry_logic"`
	ExitLogic     *ExitLogic  `json:"exit_logic"`
	StopLoss      float64     `json:"stop_loss,omitempty"`
	TakeProfit    float64     `json:"take_profit,omitempty"`
	FirstSeenTime int64       `json:"first_seen_time,omitempty"` // 持仓首次出现时间（Unix毫秒时间戳）
}

// EntryLogic 进场逻辑
type EntryLogic struct {
	Reasoning      string                 `json:"reasoning"`
	Conditions     []LogicCondition      `json:"conditions"`
	MultiTimeframe *MultiTimeframeLogic  `json:"multi_timeframe,omitempty"`
	Timestamp      time.Time              `json:"timestamp"`
}

// ExitLogic 出场逻辑
type ExitLogic struct {
	Reasoning      string                 `json:"reasoning"`
	Conditions     []LogicCondition      `json:"conditions"`
	MultiTimeframe *MultiTimeframeLogic  `json:"multi_timeframe,omitempty"`
	Timestamp      time.Time              `json:"timestamp"`
}

// LogicCondition 逻辑条件
type LogicCondition struct {
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Timeframe   string  `json:"timeframe,omitempty"`
	Value       float64 `json:"value,omitempty"`
	Operator    string  `json:"operator,omitempty"`
}

// MultiTimeframeLogic 多时间框架逻辑
type MultiTimeframeLogic struct {
	MajorTrend    string            `json:"major_trend"`
	PullbackEntry bool              `json:"pullback_entry"`
	Timeframes    map[string]string `json:"timeframes"`
}

// SaveEntryLogic 保存进场逻辑
func (s *PositionLogicStorage) SaveEntryLogic(symbol, side string, entryLogic *EntryLogic) error {
	entryLogicJSON, err := json.Marshal(entryLogic)
	if err != nil {
		return fmt.Errorf("序列化进场逻辑失败: %w", err)
	}

	query := `
		INSERT INTO position_logic (symbol, side, entry_logic, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, side) DO UPDATE SET
			entry_logic = excluded.entry_logic,
			updated_at = excluded.updated_at
	`

	_, err = s.db.Exec(query, symbol, side, string(entryLogicJSON), time.Now())
	if err != nil {
		return fmt.Errorf("保存进场逻辑失败: %w", err)
	}

	return nil
}

// SaveExitLogic 保存出场逻辑
func (s *PositionLogicStorage) SaveExitLogic(symbol, side string, exitLogic *ExitLogic) error {
	exitLogicJSON, err := json.Marshal(exitLogic)
	if err != nil {
		return fmt.Errorf("序列化出场逻辑失败: %w", err)
	}

	query := `
		INSERT INTO position_logic (symbol, side, exit_logic, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, side) DO UPDATE SET
			exit_logic = excluded.exit_logic,
			updated_at = excluded.updated_at
	`

	_, err = s.db.Exec(query, symbol, side, string(exitLogicJSON), time.Now())
	if err != nil {
		return fmt.Errorf("保存出场逻辑失败: %w", err)
	}

	return nil
}

// GetLogic 获取持仓逻辑
func (s *PositionLogicStorage) GetLogic(symbol, side string) (*PositionLogic, error) {
	query := `
		SELECT entry_logic, exit_logic, stop_loss, take_profit, first_seen_time
		FROM position_logic
		WHERE symbol = ? AND side = ?
	`

	var entryLogicJSON, exitLogicJSON sql.NullString
	var stopLoss, takeProfit sql.NullFloat64
	var firstSeenTime sql.NullInt64

	err := s.db.QueryRow(query, symbol, side).Scan(
		&entryLogicJSON, &exitLogicJSON, &stopLoss, &takeProfit, &firstSeenTime,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询持仓逻辑失败: %w", err)
	}

	logic := &PositionLogic{}

	if entryLogicJSON.Valid {
		var entryLogic EntryLogic
		if err := json.Unmarshal([]byte(entryLogicJSON.String), &entryLogic); err != nil {
			log.Printf("⚠️  解析进场逻辑失败: %v", err)
		} else {
			logic.EntryLogic = &entryLogic
		}
	}

	if exitLogicJSON.Valid {
		var exitLogic ExitLogic
		if err := json.Unmarshal([]byte(exitLogicJSON.String), &exitLogic); err != nil {
			log.Printf("⚠️  解析出场逻辑失败: %v", err)
		} else {
			logic.ExitLogic = &exitLogic
		}
	}

	if stopLoss.Valid {
		logic.StopLoss = stopLoss.Float64
	}

	if takeProfit.Valid {
		logic.TakeProfit = takeProfit.Float64
	}

	if firstSeenTime.Valid {
		logic.FirstSeenTime = firstSeenTime.Int64
	}

	return logic, nil
}

// SaveStopLoss 保存止损价格
func (s *PositionLogicStorage) SaveStopLoss(symbol, side string, stopLoss float64) error {
	query := `
		INSERT INTO position_logic (symbol, side, stop_loss, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, side) DO UPDATE SET
			stop_loss = excluded.stop_loss,
			updated_at = excluded.updated_at
	`

	_, err := s.db.Exec(query, symbol, side, stopLoss, time.Now())
	if err != nil {
		return fmt.Errorf("保存止损价格失败: %w", err)
	}

	return nil
}

// SaveTakeProfit 保存止盈价格
func (s *PositionLogicStorage) SaveTakeProfit(symbol, side string, takeProfit float64) error {
	query := `
		INSERT INTO position_logic (symbol, side, take_profit, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, side) DO UPDATE SET
			take_profit = excluded.take_profit,
			updated_at = excluded.updated_at
	`

	_, err := s.db.Exec(query, symbol, side, takeProfit, time.Now())
	if err != nil {
		return fmt.Errorf("保存止盈价格失败: %w", err)
	}

	return nil
}

// SaveStopLossAndTakeProfit 同时保存止损和止盈价格
func (s *PositionLogicStorage) SaveStopLossAndTakeProfit(symbol, side string, stopLoss, takeProfit float64) error {
	// 先获取现有记录
	logic, err := s.GetLogic(symbol, side)
	if err != nil {
		return err
	}

	// 如果记录不存在，创建新记录
	if logic == nil {
		logic = &PositionLogic{}
	}

	// 只更新提供的价格（>0），否则保持原有值
	if stopLoss > 0 {
		logic.StopLoss = stopLoss
	}
	if takeProfit > 0 {
		logic.TakeProfit = takeProfit
	}

	query := `
		INSERT INTO position_logic (symbol, side, stop_loss, take_profit, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(symbol, side) DO UPDATE SET
			stop_loss = excluded.stop_loss,
			take_profit = excluded.take_profit,
			updated_at = excluded.updated_at
	`

	_, err = s.db.Exec(query, symbol, side, logic.StopLoss, logic.TakeProfit, time.Now())
	if err != nil {
		return fmt.Errorf("保存止损和止盈价格失败: %w", err)
	}

	return nil
}

// DeleteLogic 删除持仓逻辑（平仓后调用）
func (s *PositionLogicStorage) DeleteLogic(symbol, side string) error {
	query := `DELETE FROM position_logic WHERE symbol = ? AND side = ?`

	_, err := s.db.Exec(query, symbol, side)
	if err != nil {
		return fmt.Errorf("删除持仓逻辑失败: %w", err)
	}

	return nil
}

// SaveFirstSeenTime 保存持仓首次出现时间
func (s *PositionLogicStorage) SaveFirstSeenTime(symbol, side string, firstSeenTime int64) error {
	query := `
		INSERT INTO position_logic (symbol, side, first_seen_time, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, side) DO UPDATE SET
			first_seen_time = excluded.first_seen_time,
			updated_at = excluded.updated_at
	`

	_, err := s.db.Exec(query, symbol, side, firstSeenTime, time.Now())
	if err != nil {
		return fmt.Errorf("保存持仓首次出现时间失败: %w", err)
	}

	return nil
}

// GetAllFirstSeenTimes 获取所有持仓的首次出现时间（用于迁移）
func (s *PositionLogicStorage) GetAllFirstSeenTimes() (map[string]int64, error) {
	query := `SELECT symbol, side, first_seen_time FROM position_logic WHERE first_seen_time > 0`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("查询持仓首次出现时间失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var symbol, side string
		var firstSeenTime int64
		if err := rows.Scan(&symbol, &side, &firstSeenTime); err != nil {
			log.Printf("⚠️  扫描持仓首次出现时间失败: %v", err)
			continue
		}
		posKey := symbol + "_" + side
		result[posKey] = firstSeenTime
	}

	return result, nil
}

