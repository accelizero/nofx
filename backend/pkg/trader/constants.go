package trader

import "time"

// 风控相关常量
const (
	// MarginSafety 保证金安全相关
	MaxMarginUsagePct            = 90.0  // 最大保证金使用率（多个币种时，%）
	MaxMarginUsagePctSingleSymbol = 80.0  // 最大保证金使用率（单个币种时，%）
	MinReserveBalancePct         = 5.0   // 最小保留余额（占总净值的%）
	MinSafeDistancePct           = 3.0   // 强制平仓价格最小安全距离（%）
	MinStopLossDistancePct       = 2.0  // 止损价最小安全距离（%）
	MaintenanceMarginRate        = 0.01  // 维持保证金率（1%）

	// PositionStopLoss 单仓位止损相关
	PositionStopLossRetryTimeout = 5 * time.Minute // 平仓失败后重试超时时间
)

// 交易相关常量
const (
	// MinPositionSizeUSD 最小仓位大小（USDT）
	MinPositionSizeUSD = 0.001
)

