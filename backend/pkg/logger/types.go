package logger

import "time"

// DecisionRecord 决策记录
type DecisionRecord struct {
	Timestamp      time.Time          `json:"timestamp"`       // 决策时间
	CycleNumber    int                `json:"cycle_number"`    // 周期编号
	InputPrompt    string             `json:"input_prompt"`    // 发送给AI的输入prompt
	CoTTrace       string             `json:"cot_trace"`       // AI思维链（输出）
	DecisionJSON   string             `json:"decision_json"`   // 决策JSON
	AccountState   AccountSnapshot    `json:"account_state"`   // 账户状态快照
	Positions      []PositionSnapshot `json:"positions"`       // 持仓快照
	CandidateCoins []string           `json:"candidate_coins"` // 候选币种列表
	Decisions      []DecisionAction   `json:"decisions"`       // 执行的决策
	ExecutionLog   []string           `json:"execution_log"`    // 执行日志
	Success        bool               `json:"success"`         // 是否成功
	ErrorMessage   string             `json:"error_message"`   // 错误信息（如果有）
}

// AccountSnapshot 账户状态快照
// 注意：字段名与实际存储的值略有不同，这是为了保持向后兼容性
type AccountSnapshot struct {
	// TotalBalance 实际存储的是 TotalEquity（账户净值 = wallet_balance + unrealized_profit）
	// 字段名保留为TotalBalance是为了API兼容性
	TotalBalance float64 `json:"total_balance"`

	AvailableBalance float64 `json:"available_balance"` // 可用余额

	// TotalUnrealizedProfit 实际存储的是 TotalPnL（总盈亏 = total_equity - initial_balance）
	// 字段名保留为TotalUnrealizedProfit是为了API兼容性
	// 注意：这不是未实现盈亏（unrealized_profit），而是相对初始余额的总盈亏
	TotalUnrealizedProfit float64 `json:"total_unrealized_profit"`

	PositionCount int     `json:"position_count"`    // 持仓数量
	MarginUsedPct float64 `json:"margin_used_pct"`   // 保证金使用率
}

// PositionSnapshot 持仓快照
type PositionSnapshot struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"`
	PositionAmt      float64 `json:"position_amt"`
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	UnrealizedProfit float64 `json:"unrealized_profit"`
	Leverage         float64 `json:"leverage"`
	LiquidationPrice float64 `json:"liquidation_price"`
}

// DecisionAction 决策动作
type DecisionAction struct {
	Action       string    `json:"action"`        // open_long, open_short, close_long, close_short
	Symbol       string    `json:"symbol"`        // 币种
	Quantity     float64   `json:"quantity"`      // 数量
	Leverage     int       `json:"leverage"`      // 杠杆（开仓时）
	Price        float64   `json:"price"`         // 执行价格
	OrderID      int64     `json:"order_id"`      // 订单ID
	Timestamp    time.Time `json:"timestamp"`     // 执行时间
	Success      bool      `json:"success"`       // 是否成功
	Error        string    `json:"error"`         // 错误信息
	IsForced     bool      `json:"is_forced"`     // 是否强制平仓
	ForcedReason string    `json:"forced_reason"` // 强制平仓原因（如果is_forced为true）
}

// TradeRecord 单笔完整交易记录（开仓+平仓配对）
type TradeRecord struct {
	// 交易标识
	TradeID string `json:"trade_id"` // 交易唯一ID (symbol_side_timestamp)
	Symbol  string `json:"symbol"`   // 币种
	Side    string `json:"side"`     // long/short

	// 开仓信息
	OpenTime     time.Time `json:"open_time"`      // 开仓时间
	OpenPrice    float64   `json:"open_price"`     // 开仓价格
	OpenQuantity float64   `json:"open_quantity"`  // 开仓数量
	OpenLeverage int       `json:"open_leverage"`  // 开仓杠杆
	OpenOrderID  int64     `json:"open_order_id"`  // 开仓订单ID
	OpenReason   string    `json:"open_reason"`    // 开仓原因（AI推理）
	OpenCycleNum int       `json:"open_cycle_num"` // 开仓时的周期编号

	// 平仓信息
	CloseTime     time.Time `json:"close_time"`      // 平仓时间
	ClosePrice    float64   `json:"close_price"`     // 平仓价格
	CloseQuantity float64   `json:"close_quantity"`  // 平仓数量（通常等于开仓数量）
	CloseOrderID  int64     `json:"close_order_id"`  // 平仓订单ID
	CloseReason   string    `json:"close_reason"`    // 平仓原因（AI推理或强制止损）
	CloseCycleNum int       `json:"close_cycle_num"` // 平仓时的周期编号
	IsForced      bool      `json:"is_forced"`      // 是否强制平仓
	ForcedReason  string    `json:"forced_reason"`   // 强制平仓原因（如果is_forced为true）

	// 交易结果
	Duration      string  `json:"duration"`       // 持仓时长
	PositionValue float64 `json:"position_value"` // 仓位价值（quantity × openPrice）
	MarginUsed    float64 `json:"margin_used"`     // 保证金使用（positionValue / leverage）
	PnL           float64 `json:"pn_l"`            // 盈亏（USDT）
	PnLPct        float64 `json:"pn_l_pct"`        // 盈亏百分比（相对保证金）

	// 附加信息
	WasStopLoss bool   `json:"was_stop_loss"` // 是否止损（亏损且强制平仓）
	Success     bool   `json:"success"`       // 是否成功（开仓和平仓都成功）
	Error       string `json:"error"`         // 错误信息（如果有）
}

// Statistics 统计信息
type Statistics struct {
	TotalCycles         int `json:"total_cycles"`
	SuccessfulCycles    int `json:"successful_cycles"`
	FailedCycles        int `json:"failed_cycles"`
	TotalOpenPositions  int `json:"total_open_positions"`
	TotalClosePositions int `json:"total_close_positions"`
}

// TradeOutcome 单笔交易结果
type TradeOutcome struct {
	Symbol        string    `json:"symbol"`         // 币种
	Side          string    `json:"side"`           // long/short
	Quantity      float64   `json:"quantity"`       // 仓位数量
	Leverage      int       `json:"leverage"`       // 杠杆倍数
	OpenPrice     float64   `json:"open_price"`     // 开仓价
	ClosePrice    float64   `json:"close_price"`    // 平仓价
	PositionValue float64   `json:"position_value"` // 仓位价值（quantity × openPrice）
	MarginUsed    float64   `json:"margin_used"`    // 保证金使用（positionValue / leverage）
	PnL           float64   `json:"pn_l"`           // 盈亏（USDT）
	PnLPct        float64   `json:"pn_l_pct"`       // 盈亏百分比（相对保证金）
	Duration      string    `json:"duration"`       // 持仓时长
	OpenTime      time.Time `json:"open_time"`       // 开仓时间
	CloseTime     time.Time `json:"close_time"`      // 平仓时间
	WasStopLoss   bool      `json:"was_stop_loss"`   // 是否止损
}

// PerformanceAnalysis 交易表现分析
type PerformanceAnalysis struct {
	TotalTrades   int                           `json:"total_trades"`   // 总交易数
	WinningTrades int                           `json:"winning_trades"` // 盈利交易数
	LosingTrades  int                           `json:"losing_trades"`  // 亏损交易数
	WinRate       float64                       `json:"win_rate"`       // 胜率
	AvgWin        float64                       `json:"avg_win"`        // 平均盈利
	AvgLoss       float64                       `json:"avg_loss"`       // 平均亏损
	ProfitFactor  float64                       `json:"profit_factor"`  // 盈亏比
	SharpeRatio   float64                       `json:"sharpe_ratio"`   // 夏普比率（风险调整后收益）
	RecentTrades  []TradeOutcome                `json:"recent_trades"`  // 最近N笔交易
	SymbolStats   map[string]*SymbolPerformance `json:"symbol_stats"`   // 各币种表现
	BestSymbol    string                        `json:"best_symbol"`    // 表现最好的币种
	WorstSymbol   string                        `json:"worst_symbol"`   // 表现最差的币种
}

// SymbolPerformance 币种表现统计
type SymbolPerformance struct {
	Symbol        string  `json:"symbol"`         // 币种
	TotalTrades   int     `json:"total_trades"`   // 交易次数
	WinningTrades int     `json:"winning_trades"` // 盈利次数
	LosingTrades  int     `json:"losing_trades"`  // 亏损次数
	WinRate       float64 `json:"win_rate"`       // 胜率
	TotalPnL      float64 `json:"total_pn_l"`     // 总盈亏
	AvgPnL        float64 `json:"avg_pn_l"`       // 平均盈亏
}

// MarketEnvironmentSnapshot 市场环境快照
// 记录当前市场的整体状态（趋势、波动率、情绪等）
type MarketEnvironmentSnapshot struct {
	// BTC/ETH基准指标（市场整体趋势）
	BTCPrice    float64 `json:"btc_price"`
	BTCChange1h float64 `json:"btc_change_1h"`
	BTCChange4h float64 `json:"btc_change_4h"`
	BTCEMA20    float64 `json:"btc_ema20"`
	BTCMACD     float64 `json:"btc_macd"`
	BTCRSI7     float64 `json:"btc_rsi7"`
	BTCRSI14    float64 `json:"btc_rsi14"`

	ETHPrice    float64 `json:"eth_price"`
	ETHChange1h float64 `json:"eth_change_1h"`
	ETHChange4h float64 `json:"eth_change_4h"`
	ETHEMA20    float64 `json:"eth_ema20"`
	ETHMACD     float64 `json:"eth_macd"`
	ETHRSI7     float64 `json:"eth_rsi7"`

	// 市场整体状态
	MarketTrend         string                  `json:"market_trend"`          // bullish/bearish/neutral/choppy
	MarketVolatility    string                  `json:"market_volatility"`     // low/medium/high/extreme
	VolatilityIndex     float64                 `json:"volatility_index"`      // 0-100的波动率指数
	TimeframeConsistency *TimeframeConsistency `json:"timeframe_consistency"` // 时间框架一致性
}

// TimeframeConsistency 时间框架一致性
type TimeframeConsistency struct {
	Trend3m     string  `json:"trend_3m"`      // up/down/sideways
	Trend1h     string  `json:"trend_1h"`      // up/down/sideways
	Trend4h     string  `json:"trend_4h"`      // up/down/sideways
	Consistency float64 `json:"consistency"`    // 一致性分数 (0-1)
	RSI3m       float64 `json:"rsi_3m"`        // 3分钟RSI
	RSI4h       float64 `json:"rsi_4h"`        // 4小时RSI
	MACD3m      float64 `json:"macd_3m"`       // 3分钟MACD
	MACD4h      float64 `json:"macd_4h"`       // 4小时MACD
}

