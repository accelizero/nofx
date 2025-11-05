package storage

import (
	"database/sql"
	"fmt"
	"log"
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
		close_time DATETIME NOT NULL,
		close_price REAL NOT NULL,
		close_quantity REAL NOT NULL,
		close_order_id INTEGER NOT NULL,
		close_reason TEXT,
		close_cycle_num INTEGER NOT NULL,
		is_forced INTEGER NOT NULL DEFAULT 0,
		forced_reason TEXT,
		duration TEXT,
		position_value REAL NOT NULL,
		margin_used REAL NOT NULL,
		pnl REAL NOT NULL,
		pnl_pct REAL NOT NULL,
		was_stop_loss INTEGER NOT NULL DEFAULT 0,
		success INTEGER NOT NULL DEFAULT 0,
		error TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	
	CREATE INDEX IF NOT EXISTS idx_symbol ON trades(symbol);
	CREATE INDEX IF NOT EXISTS idx_close_time ON trades(close_time);
	CREATE INDEX IF NOT EXISTS idx_open_time ON trades(open_time);
	`

	_, err := s.db.Exec(createTableSQL)
	return err
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
	CloseTime      time.Time `json:"close_time"`
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
	WasStopLoss    bool      `json:"was_stop_loss"`
	Success        bool      `json:"success"`
	Error          string    `json:"error"`
}

// LogTrade 记录一笔完整交易
func (s *TradeStorage) LogTrade(trade *TradeRecord) error {
	query := `
		INSERT INTO trades (
			trade_id, symbol, side, open_time, open_price, open_quantity,
			open_leverage, open_order_id, open_reason, open_cycle_num,
			close_time, close_price, close_quantity, close_order_id,
			close_reason, close_cycle_num, is_forced, forced_reason,
			duration, position_value, margin_used, pnl, pnl_pct,
			was_stop_loss, success, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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

	_, err := s.db.Exec(query,
		trade.TradeID, trade.Symbol, trade.Side,
		trade.OpenTime, trade.OpenPrice, trade.OpenQuantity,
		trade.OpenLeverage, trade.OpenOrderID, trade.OpenReason, trade.OpenCycleNum,
		trade.CloseTime, trade.ClosePrice, trade.CloseQuantity,
		trade.CloseOrderID, trade.CloseReason, trade.CloseCycleNum,
		isForced, trade.ForcedReason,
		trade.Duration, trade.PositionValue, trade.MarginUsed,
		trade.PnL, trade.PnLPct,
		wasStopLoss, success, trade.Error,
	)

	if err != nil {
		return fmt.Errorf("保存交易记录失败: %w", err)
	}

	return nil
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

// GetLatestTrades 获取最近N笔交易
func (s *TradeStorage) GetLatestTrades(n int) ([]*TradeRecord, error) {
	query := `
		SELECT * FROM trades
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

// GetTradesBySymbol 获取指定币种的所有交易
func (s *TradeStorage) GetTradesBySymbol(symbol string, days int) ([]*TradeRecord, error) {
	cutoffDate := time.Now().AddDate(0, 0, -days)

	query := `
		SELECT * FROM trades
		WHERE symbol = ? AND close_time >= ?
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
		trade := &TradeRecord{}
		var isForced, wasStopLoss, success int
		var createdAt sql.NullTime

		err := rows.Scan(
			&trade.TradeID, &trade.Symbol, &trade.Side,
			&trade.OpenTime, &trade.OpenPrice, &trade.OpenQuantity,
			&trade.OpenLeverage, &trade.OpenOrderID, &trade.OpenReason, &trade.OpenCycleNum,
			&trade.CloseTime, &trade.ClosePrice, &trade.CloseQuantity,
			&trade.CloseOrderID, &trade.CloseReason, &trade.CloseCycleNum,
			&isForced, &trade.ForcedReason,
			&trade.Duration, &trade.PositionValue, &trade.MarginUsed,
			&trade.PnL, &trade.PnLPct,
			&wasStopLoss, &success, &trade.Error,
			&createdAt,
		)

		if err != nil {
			log.Printf("⚠️  扫描交易记录失败: %v", err)
			continue
		}

		trade.IsForced = isForced == 1
		trade.WasStopLoss = wasStopLoss == 1
		trade.Success = success == 1

		trades = append(trades, trade)
	}

	return trades, rows.Err()
}

