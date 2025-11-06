package storage

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"backend/pkg/db"
	"time"
)

// TradeStorage 交易记录存储（使用SQLite）
type TradeStorage struct {
	dbManager *db.DBManager
	db        *sql.DB
}

// NewTradeStorage 创建交易记录存储
func NewTradeStorage(dbManager *db.DBManager) (*TradeStorage, error) {
	storage := &TradeStorage{
		dbManager: dbManager,
	}

	// 获取数据库连接
	database, err := dbManager.GetDB("trade_history")
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
func (s *TradeStorage) initTable() error {
	// 先创建表（如果不存在）
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS trades (
		trade_id TEXT PRIMARY KEY,
		symbol TEXT NOT NULL,
		side TEXT NOT NULL,
		open_time DATETIME NOT NULL,
		open_price REAL NOT NULL,
		open_quantity REAL NOT NULL,
		open_leverage INTEGER NOT NULL,
		open_order_id INTEGER NOT NULL,
		open_reason TEXT,
		open_cycle_num INTEGER NOT NULL,
		close_time DATETIME,
		close_price REAL DEFAULT 0,
		close_quantity REAL DEFAULT 0,
		close_order_id INTEGER DEFAULT 0,
		close_reason TEXT,
		close_cycle_num INTEGER DEFAULT 0,
		is_forced INTEGER NOT NULL DEFAULT 0,
		forced_reason TEXT,
		duration TEXT,
		position_value REAL NOT NULL,
		margin_used REAL NOT NULL,
		pnl REAL DEFAULT 0,
		pnl_pct REAL DEFAULT 0,
		was_stop_loss INTEGER NOT NULL DEFAULT 0,
		success INTEGER NOT NULL DEFAULT 0,
		error TEXT,
		entry_logic TEXT,
		exit_logic TEXT,
		update_sl_logic TEXT,
		update_tp_logic TEXT,
		close_logic TEXT,
		forced_close_logic TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(symbol, open_time)
	);
	
	CREATE INDEX IF NOT EXISTS idx_symbol ON trades(symbol);
	CREATE INDEX IF NOT EXISTS idx_close_time ON trades(close_time);
	CREATE INDEX IF NOT EXISTS idx_open_time ON trades(open_time);
	CREATE INDEX IF NOT EXISTS idx_symbol_open_time ON trades(symbol, open_time);
	`

	_, err := s.db.Exec(createTableSQL)
	if err != nil {
		return err
	}

	// 迁移现有数据库：添加新字段（如果不存在）
	migrationSQL := []string{
		// 检查并添加entry_logic字段
		`ALTER TABLE trades ADD COLUMN entry_logic TEXT;`,
		// 检查并添加exit_logic字段
		`ALTER TABLE trades ADD COLUMN exit_logic TEXT;`,
		// 检查并添加update_sl_logic字段
		`ALTER TABLE trades ADD COLUMN update_sl_logic TEXT;`,
		// 检查并添加update_tp_logic字段
		`ALTER TABLE trades ADD COLUMN update_tp_logic TEXT;`,
		// 检查并添加close_logic字段
		`ALTER TABLE trades ADD COLUMN close_logic TEXT;`,
		// 检查并添加forced_close_logic字段
		`ALTER TABLE trades ADD COLUMN forced_close_logic TEXT;`,
		// 检查并添加updated_at字段
		`ALTER TABLE trades ADD COLUMN updated_at DATETIME DEFAULT CURRENT_TIMESTAMP;`,
		// 修改close_time等字段允许NULL（已开仓但未平仓的记录）
		// SQLite不支持直接修改列，这里只处理新增列的情况
	}

	for _, sql := range migrationSQL {
		// SQLite的ALTER TABLE ADD COLUMN如果列已存在会报错，忽略错误
		if _, err := s.db.Exec(sql); err != nil {
			// 检查是否是"列已存在"的错误
			errStr := err.Error()
			if !strings.Contains(errStr, "duplicate column") && 
			   !strings.Contains(errStr, "already exists") &&
			   !strings.Contains(errStr, "UNIQUE constraint failed") {
				// 如果是其他错误，记录日志但不中断
				log.Printf("⚠️  数据库迁移警告: %v (SQL: %s)", err, sql)
			}
			// 如果是列已存在，忽略错误
		}
	}

	return nil
}

// TradeRecord 单笔完整交易记录
type TradeRecord struct {
	TradeID        string    `json:"trade_id"`
	Symbol         string    `json:"symbol"`
	Side           string    `json:"side"`
	OpenTime       time.Time `json:"open_time"`
	OpenPrice      float64   `json:"open_price"`
	OpenQuantity   float64   `json:"open_quantity"`
	OpenLeverage   int       `json:"open_leverage"`
	OpenOrderID    int64     `json:"open_order_id"`
	OpenReason     string    `json:"open_reason"`
	OpenCycleNum   int       `json:"open_cycle_num"`
	CloseTime      *time.Time `json:"close_time,omitempty"` // 允许为NULL，表示未平仓
	ClosePrice     float64   `json:"close_price"`
	CloseQuantity  float64   `json:"close_quantity"`
	CloseOrderID   int64     `json:"close_order_id"`
	CloseReason    string    `json:"close_reason"`
	CloseCycleNum  int       `json:"close_cycle_num"`
	IsForced       bool      `json:"is_forced"`
	ForcedReason   string    `json:"forced_reason"`
	Duration       string    `json:"duration"`
	PositionValue  float64   `json:"position_value"`
	MarginUsed     float64   `json:"margin_used"`
	PnL            float64   `json:"pn_l"`
	PnLPct         float64   `json:"pn_l_pct"`
	WasStopLoss      bool       `json:"was_stop_loss"`
	Success          bool       `json:"success"`
	Error            string     `json:"error"`
	EntryLogic       string     `json:"entry_logic"`        // 进场逻辑
	ExitLogic        string     `json:"exit_logic"`         // 出场逻辑（开仓时规划的）
	UpdateSLLogic    string     `json:"update_sl_logic"`    // 更新止损逻辑
	UpdateTPLogic    string     `json:"update_tp_logic"`    // 更新止盈逻辑
	CloseLogic       string     `json:"close_logic"`        // 平仓逻辑（直接平仓的理由）
	ForcedCloseLogic string     `json:"forced_close_logic"` // 强制平仓逻辑
}

// LogTrade 记录一笔完整交易（向后兼容，用于平仓时一次性写入）
func (s *TradeStorage) LogTrade(trade *TradeRecord) error {
	query := `
		INSERT INTO trades (
			trade_id, symbol, side, open_time, open_price, open_quantity,
			open_leverage, open_order_id, open_reason, open_cycle_num,
			close_time, close_price, close_quantity, close_order_id,
			close_reason, close_cycle_num, is_forced, forced_reason,
			duration, position_value, margin_used, pnl, pnl_pct,
			was_stop_loss, success, error, entry_logic, exit_logic,
			update_sl_logic, update_tp_logic, close_logic, forced_close_logic
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	isForced := 0
	if trade.IsForced {
		isForced = 1
	}
	wasStopLoss := 0
	if trade.WasStopLoss {
		wasStopLoss = 1
	}
	success := 0
	if trade.Success {
		success = 1
	}

	var closeTime interface{}
	if trade.CloseTime != nil {
		closeTime = *trade.CloseTime
	}

	_, err := s.db.Exec(query,
		trade.TradeID, trade.Symbol, trade.Side,
		trade.OpenTime, trade.OpenPrice, trade.OpenQuantity,
		trade.OpenLeverage, trade.OpenOrderID, trade.OpenReason, trade.OpenCycleNum,
		closeTime, trade.ClosePrice, trade.CloseQuantity,
		trade.CloseOrderID, trade.CloseReason, trade.CloseCycleNum,
		isForced, trade.ForcedReason,
		trade.Duration, trade.PositionValue, trade.MarginUsed,
		trade.PnL, trade.PnLPct,
		wasStopLoss, success, trade.Error,
		trade.EntryLogic, trade.ExitLogic,
		trade.UpdateSLLogic, trade.UpdateTPLogic, trade.CloseLogic, trade.ForcedCloseLogic,
	)

	if err != nil {
		return fmt.Errorf("保存交易记录失败: %w", err)
	}

	return nil
}

// CreateOrUpdateTrade 创建或更新交易记录（建仓时创建，后续操作更新）
// 如果记录不存在则创建，存在则更新
func (s *TradeStorage) CreateOrUpdateTrade(trade *TradeRecord) error {
	// 检查记录是否存在
	var exists bool
	err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM trades WHERE symbol = ? AND open_time = ?)",
		trade.Symbol, trade.OpenTime,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("检查交易记录是否存在失败: %w", err)
	}

	if exists {
		// 更新现有记录
		return s.UpdateTrade(trade)
	} else {
		// 创建新记录
		return s.CreateTrade(trade)
	}
}

// CreateTrade 创建新的交易记录（建仓时调用）
func (s *TradeStorage) CreateTrade(trade *TradeRecord) error {
	query := `
		INSERT INTO trades (
			trade_id, symbol, side, open_time, open_price, open_quantity,
			open_leverage, open_order_id, open_reason, open_cycle_num,
			position_value, margin_used, entry_logic, exit_logic,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`

	_, err := s.db.Exec(query,
		trade.TradeID, trade.Symbol, trade.Side,
		trade.OpenTime, trade.OpenPrice, trade.OpenQuantity,
		trade.OpenLeverage, trade.OpenOrderID, trade.OpenReason, trade.OpenCycleNum,
		trade.PositionValue, trade.MarginUsed,
		trade.EntryLogic, trade.ExitLogic,
	)

	if err != nil {
		return fmt.Errorf("创建交易记录失败: %w", err)
	}

	return nil
}

// UpdateTrade 更新交易记录（update_sl、update_tp、平仓时调用）
func (s *TradeStorage) UpdateTrade(trade *TradeRecord) error {
	// 构建更新SQL，只更新非空字段
	updates := []string{"updated_at = CURRENT_TIMESTAMP"}
	args := []interface{}{}

	if trade.UpdateSLLogic != "" {
		updates = append(updates, "update_sl_logic = ?")
		args = append(args, trade.UpdateSLLogic)
	}

	if trade.UpdateTPLogic != "" {
		updates = append(updates, "update_tp_logic = ?")
		args = append(args, trade.UpdateTPLogic)
	}

	// 如果提供了平仓信息，更新平仓相关字段
	if trade.CloseTime != nil {
		// 强制平仓和平仓逻辑应该互斥
		if trade.IsForced {
			// 强制平仓时，只更新forced_close_logic
			if trade.ForcedCloseLogic != "" {
				updates = append(updates, "forced_close_logic = ?")
				args = append(args, trade.ForcedCloseLogic)
			}
			// 不更新close_logic（强制平仓不应该有主动平仓逻辑）
		} else {
			// 主动平仓时，只更新close_logic
			if trade.CloseLogic != "" {
				updates = append(updates, "close_logic = ?")
				args = append(args, trade.CloseLogic)
			}
		}
		updates = append(updates, "close_time = ?", "close_price = ?", "close_quantity = ?",
			"close_order_id = ?", "close_reason = ?", "close_cycle_num = ?",
			"is_forced = ?", "forced_reason = ?", "duration = ?",
			"pnl = ?", "pnl_pct = ?", "was_stop_loss = ?", "success = ?", "error = ?")
		
		isForced := 0
		if trade.IsForced {
			isForced = 1
		}
		wasStopLoss := 0
		if trade.WasStopLoss {
			wasStopLoss = 1
		}
		success := 0
		if trade.Success {
			success = 1
		}

		args = append(args, *trade.CloseTime, trade.ClosePrice, trade.CloseQuantity,
			trade.CloseOrderID, trade.CloseReason, trade.CloseCycleNum,
			isForced, trade.ForcedReason, trade.Duration,
			trade.PnL, trade.PnLPct, wasStopLoss, success, trade.Error)
	}

	if len(updates) <= 1 {
		// 只有updated_at，无需更新
		return nil
	}

	query := fmt.Sprintf(
		"UPDATE trades SET %s WHERE symbol = ? AND open_time = ?",
		strings.Join(updates, ", "),
	)
	args = append(args, trade.Symbol, trade.OpenTime)

	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("更新交易记录失败: %w", err)
	}

	return nil
}

// GetOpenTrade 获取未平仓的交易记录（根据symbol和side）
func (s *TradeStorage) GetOpenTrade(symbol, side string) (*TradeRecord, error) {
	query := `
		SELECT * FROM trades
		WHERE symbol = ? AND side = ? AND close_time IS NULL
		ORDER BY open_time DESC
		LIMIT 1
	`

	row := s.db.QueryRow(query, symbol, side)
	trade, err := s.scanTrade(row)
	if err == sql.ErrNoRows {
		return nil, nil // 未找到记录
	}
	if err != nil {
		return nil, fmt.Errorf("查询未平仓交易记录失败: %w", err)
	}

	return trade, nil
}

// GetOpenTradeByTime 根据开仓时间获取交易记录（使用时间范围查询，避免精确匹配失败）
func (s *TradeStorage) GetOpenTradeByTime(symbol string, openTime time.Time) (*TradeRecord, error) {
	// 使用时间范围查询（前后10秒），避免精确匹配失败（交易所时间戳和数据库时间可能有微小差异）
	startTime := openTime.Add(-10 * time.Second)
	endTime := openTime.Add(10 * time.Second)
	
	query := `
		SELECT * FROM trades
		WHERE symbol = ? AND open_time >= ? AND open_time <= ?
		ORDER BY ABS((julianday(open_time) - julianday(?)) * 86400) ASC
		LIMIT 1
	`

	row := s.db.QueryRow(query, symbol, startTime, endTime, openTime)
	trade, err := s.scanTrade(row)
	if err == sql.ErrNoRows {
		return nil, nil // 未找到记录
	}
	if err != nil {
		return nil, fmt.Errorf("查询交易记录失败: %w", err)
	}

	return trade, nil
}

// GetTradesByDate 获取指定日期的所有交易
func (s *TradeStorage) GetTradesByDate(date time.Time) ([]*TradeRecord, error) {
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	endOfDay := startOfDay.Add(24 * time.Hour)

	query := `
		SELECT * FROM trades
		WHERE close_time >= ? AND close_time < ?
		ORDER BY close_time ASC
	`

	rows, err := s.db.Query(query, startOfDay, endOfDay)
	if err != nil {
		return nil, fmt.Errorf("查询交易记录失败: %w", err)
	}
	defer rows.Close()

	return s.scanTrades(rows)
}

// GetLatestTrades 获取最近N笔已平仓的交易
func (s *TradeStorage) GetLatestTrades(n int) ([]*TradeRecord, error) {
	query := `
		SELECT * FROM trades
		WHERE close_time IS NOT NULL
		ORDER BY close_time DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, n)
	if err != nil {
		return nil, fmt.Errorf("查询交易记录失败: %w", err)
	}
	defer rows.Close()

	trades, err := s.scanTrades(rows)
	if err != nil {
		return nil, err
	}

	// 反转顺序，最新的在前
	for i, j := 0, len(trades)-1; i < j; i, j = i+1, j-1 {
		trades[i], trades[j] = trades[j], trades[i]
	}

	return trades, nil
}

// GetTradesBySymbol 获取指定币种的所有已平仓交易
func (s *TradeStorage) GetTradesBySymbol(symbol string, days int) ([]*TradeRecord, error) {
	cutoffDate := time.Now().AddDate(0, 0, -days)

	query := `
		SELECT * FROM trades
		WHERE symbol = ? AND close_time IS NOT NULL AND close_time >= ?
		ORDER BY close_time DESC
	`

	rows, err := s.db.Query(query, symbol, cutoffDate)
	if err != nil {
		return nil, fmt.Errorf("查询交易记录失败: %w", err)
	}
	defer rows.Close()

	return s.scanTrades(rows)
}

// scanTrades 扫描查询结果
func (s *TradeStorage) scanTrades(rows *sql.Rows) ([]*TradeRecord, error) {
	var trades []*TradeRecord

	for rows.Next() {
		trade, err := s.scanTradeRow(rows)
		if err != nil {
			log.Printf("⚠️  扫描交易记录失败: %v", err)
			continue
		}
		trades = append(trades, trade)
	}

	return trades, rows.Err()
}

// scanTrade 扫描单条记录（用于QueryRow）
func (s *TradeStorage) scanTrade(row *sql.Row) (*TradeRecord, error) {
	trade := &TradeRecord{}
	var isForced, wasStopLoss, success int
	var closeTime sql.NullTime
	var createdAt, updatedAt sql.NullTime

	err := row.Scan(
		&trade.TradeID, &trade.Symbol, &trade.Side,
		&trade.OpenTime, &trade.OpenPrice, &trade.OpenQuantity,
		&trade.OpenLeverage, &trade.OpenOrderID, &trade.OpenReason, &trade.OpenCycleNum,
		&closeTime, &trade.ClosePrice, &trade.CloseQuantity,
		&trade.CloseOrderID, &trade.CloseReason, &trade.CloseCycleNum,
		&isForced, &trade.ForcedReason,
		&trade.Duration, &trade.PositionValue, &trade.MarginUsed,
		&trade.PnL, &trade.PnLPct,
		&wasStopLoss, &success, &trade.Error,
		&trade.EntryLogic, &trade.ExitLogic,
		&trade.UpdateSLLogic, &trade.UpdateTPLogic,
		&trade.CloseLogic, &trade.ForcedCloseLogic,
		&createdAt, &updatedAt,
	)

	if err != nil {
		return nil, err
	}

	if closeTime.Valid {
		trade.CloseTime = &closeTime.Time
	}
	trade.IsForced = isForced == 1
	trade.WasStopLoss = wasStopLoss == 1
	trade.Success = success == 1

	return trade, nil
}

// scanTradeRow 扫描单行记录（用于Rows）
func (s *TradeStorage) scanTradeRow(rows *sql.Rows) (*TradeRecord, error) {
	trade := &TradeRecord{}
	var isForced, wasStopLoss, success int
	var closeTime sql.NullTime
	var createdAt, updatedAt sql.NullTime

	err := rows.Scan(
		&trade.TradeID, &trade.Symbol, &trade.Side,
		&trade.OpenTime, &trade.OpenPrice, &trade.OpenQuantity,
		&trade.OpenLeverage, &trade.OpenOrderID, &trade.OpenReason, &trade.OpenCycleNum,
		&closeTime, &trade.ClosePrice, &trade.CloseQuantity,
		&trade.CloseOrderID, &trade.CloseReason, &trade.CloseCycleNum,
		&isForced, &trade.ForcedReason,
		&trade.Duration, &trade.PositionValue, &trade.MarginUsed,
		&trade.PnL, &trade.PnLPct,
		&wasStopLoss, &success, &trade.Error,
		&trade.EntryLogic, &trade.ExitLogic,
		&trade.UpdateSLLogic, &trade.UpdateTPLogic,
		&trade.CloseLogic, &trade.ForcedCloseLogic,
		&createdAt, &updatedAt,
	)

	if err != nil {
		return nil, err
	}

	if closeTime.Valid {
		trade.CloseTime = &closeTime.Time
	}
	trade.IsForced = isForced == 1
	trade.WasStopLoss = wasStopLoss == 1
	trade.Success = success == 1

	return trade, nil
}

