package trader

import (
	"fmt"
	"backend/pkg/decision"
	"backend/pkg/logger"
	"backend/pkg/market"
	"backend/pkg/storage"
	"time"
)

// logCycleSnapshot 记录周期快照（用于自检式review）
func (at *AutoTrader) logCycleSnapshot(ctx *decision.Context, decision *decision.FullDecision, record *logger.DecisionRecord, cycleNum int64) error {
	if at.storageAdapter == nil {
		return nil
	}

	cycleSnapshotStorage := at.storageAdapter.GetCycleSnapshotStorage()
	if cycleSnapshotStorage == nil {
		return nil
	}

	// 构建市场环境快照
	marketEnv := at.buildMarketEnvironmentSnapshot(ctx)

	// 构建AI决策快照
	aiDecision := map[string]interface{}{
		"input_prompt":  decision.UserPrompt,
		"cot_trace":     decision.CoTTrace,
		"decisions":     record.Decisions,
		"decision_json": record.DecisionJSON,
	}

	// 统计决策类型
	openLongCount := 0
	openShortCount := 0
	closeLongCount := 0
	closeShortCount := 0
	waitCount := 0
	for _, action := range record.Decisions {
		switch action.Action {
		case "open_long":
			openLongCount++
		case "open_short":
			openShortCount++
		case "close_long":
			closeLongCount++
		case "close_short":
			closeShortCount++
		case "wait", "hold":
			waitCount++
		}
	}
	aiDecision["open_long_count"] = openLongCount
	aiDecision["open_short_count"] = openShortCount
	aiDecision["close_long_count"] = closeLongCount
	aiDecision["close_short_count"] = closeShortCount
	aiDecision["wait_count"] = waitCount

	// 构建执行结果快照
	execResult := map[string]interface{}{
		"total_actions": len(record.Decisions),
		"executed_actions": record.Decisions,
		"execution_errors": []string{},
		"success_count": 0,
		"failed_count": 0,
		"forced_close_count": 0,
	}

	successCount := 0
	failedCount := 0
	forcedCloseCount := 0
	var executionErrors []string
	for _, action := range record.Decisions {
		if action.Success {
			successCount++
		} else {
			failedCount++
			if action.Error != "" {
				executionErrors = append(executionErrors, fmt.Sprintf("%s %s: %s", action.Symbol, action.Action, action.Error))
			}
		}
		if action.IsForced {
			forcedCloseCount++
		}
	}
	execResult["success_count"] = successCount
	execResult["failed_count"] = failedCount
	execResult["forced_close_count"] = forcedCloseCount
	execResult["execution_errors"] = executionErrors

	// 构建系统指标
	systemMetrics := map[string]interface{}{
		"cycle_execution_time": time.Since(record.Timestamp).Milliseconds(),
	}

	// 构建周期快照
	snapshot := &storage.CycleSnapshot{
		TraderID:          at.id,
		CycleNumber:       int(cycleNum),
		Timestamp:         record.Timestamp,
		ScanInterval:      int(at.config.ScanInterval.Minutes()),
		AccountState:      record.AccountState,
		MarketEnvironment: marketEnv,
		PositionsSnapshot: record.Positions,
		AIDecision:        aiDecision,
		ExecutionResult:   execResult,
		SystemMetrics:    systemMetrics,
	}

	// 保存到数据库
	return cycleSnapshotStorage.LogCycleSnapshot(snapshot)
}

// buildMarketEnvironmentSnapshot 构建市场环境快照
func (at *AutoTrader) buildMarketEnvironmentSnapshot(ctx *decision.Context) *logger.MarketEnvironmentSnapshot {
	env := &logger.MarketEnvironmentSnapshot{}

	// 获取BTC和ETH的市场数据作为基准
	btcData, err := market.Get("BTCUSDT")
	if err == nil && btcData != nil {
		env.BTCPrice = btcData.CurrentPrice
		env.BTCChange1h = btcData.PriceChange1h
		env.BTCChange4h = btcData.PriceChange4h
		env.BTCEMA20 = btcData.CurrentEMA20
		env.BTCMACD = btcData.CurrentMACD
		env.BTCRSI7 = btcData.CurrentRSI7
		// 获取4h数据来获取RSI14
		btcData4h, err4h := market.GetWithTimeframe("BTCUSDT", "4h", 1000)
		if err4h == nil && btcData4h != nil && btcData4h.IntradaySeries != nil && len(btcData4h.IntradaySeries.RSI14Values) > 0 {
			env.BTCRSI14 = btcData4h.IntradaySeries.RSI14Values[len(btcData4h.IntradaySeries.RSI14Values)-1]
		}
	}

	ethData, err := market.Get("ETHUSDT")
	if err == nil && ethData != nil {
		env.ETHPrice = ethData.CurrentPrice
		env.ETHChange1h = ethData.PriceChange1h
		env.ETHChange4h = ethData.PriceChange4h
		env.ETHEMA20 = ethData.CurrentEMA20
		env.ETHMACD = ethData.CurrentMACD
		env.ETHRSI7 = ethData.CurrentRSI7
	}

	// 判断市场趋势（基于BTC的多个指标）
	env.MarketTrend = at.determineMarketTrend(env)
	
	// 判断市场波动率
	env.MarketVolatility, env.VolatilityIndex = at.determineMarketVolatility(env, ctx)

	// 评估时间框架一致性
	env.TimeframeConsistency = at.assessTimeframeConsistency(ctx)

	return env
}

// determineMarketTrend 判断市场趋势
func (at *AutoTrader) determineMarketTrend(env *logger.MarketEnvironmentSnapshot) string {
	// 基于BTC的多个指标综合判断
	bullishSignals := 0
	bearishSignals := 0

	// BTC价格变化
	if env.BTCChange1h > 0.3 {
		bullishSignals++
	} else if env.BTCChange1h < -0.3 {
		bearishSignals++
	}

	if env.BTCChange4h > 0.5 {
		bullishSignals++
	} else if env.BTCChange4h < -0.5 {
		bearishSignals++
	}

	// MACD信号
	if env.BTCMACD > 50 {
		bullishSignals++
	} else if env.BTCMACD < -50 {
		bearishSignals++
	}

	// RSI信号
	if env.BTCRSI7 > 60 && env.BTCRSI7 < 80 {
		bullishSignals++
	} else if env.BTCRSI7 < 40 {
		bearishSignals++
	} else if env.BTCRSI7 > 80 {
		// 超买，可能是顶部
		bearishSignals++
	}

	if bullishSignals > bearishSignals+1 {
		return "bullish"
	} else if bearishSignals > bullishSignals+1 {
		return "bearish"
	} else if bullishSignals == bearishSignals {
		return "neutral"
	}
	return "choppy" // 信号混乱
}

// determineMarketVolatility 判断市场波动率
func (at *AutoTrader) determineMarketVolatility(env *logger.MarketEnvironmentSnapshot, ctx *decision.Context) (string, float64) {
	// 基于价格变化幅度判断波动率
	maxChange := env.BTCChange1h
	if abs(env.BTCChange4h) > abs(maxChange) {
		maxChange = env.BTCChange4h
	}

	volatilityIndex := abs(maxChange) * 10 // 转换为0-100的指数

	if volatilityIndex < 10 {
		return "low", volatilityIndex
	} else if volatilityIndex < 30 {
		return "medium", volatilityIndex
	} else if volatilityIndex < 50 {
		return "high", volatilityIndex
	}
	return "extreme", volatilityIndex
}

// assessTimeframeConsistency 评估时间框架一致性
func (at *AutoTrader) assessTimeframeConsistency(ctx *decision.Context) *logger.TimeframeConsistency {
	tf := &logger.TimeframeConsistency{}

	// 获取BTC数据用于评估
	btcData, err := market.Get("BTCUSDT")
	if err != nil || btcData == nil {
		return tf
	}

	// 3分钟趋势（基于1小时变化）
	if btcData.PriceChange1h > 0.1 {
		tf.Trend3m = "up"
	} else if btcData.PriceChange1h < -0.1 {
		tf.Trend3m = "down"
	} else {
		tf.Trend3m = "sideways"
	}

	// 4小时趋势
	if btcData.PriceChange4h > 0.2 {
		tf.Trend4h = "up"
	} else if btcData.PriceChange4h < -0.2 {
		tf.Trend4h = "down"
	} else {
		tf.Trend4h = "sideways"
	}

	// 简化：1小时趋势使用4小时的一半作为近似
	if btcData.PriceChange4h > 0.15 {
		tf.Trend1h = "up"
	} else if btcData.PriceChange4h < -0.15 {
		tf.Trend1h = "down"
	} else {
		tf.Trend1h = "sideways"
	}

	// 计算一致性分数
	consistencyScore := 0.0
	if tf.Trend3m == tf.Trend4h {
		consistencyScore += 0.5
	}
	if tf.Trend1h == tf.Trend4h {
		consistencyScore += 0.3
	}
	if tf.Trend3m == tf.Trend1h {
		consistencyScore += 0.2
	}
	tf.Consistency = consistencyScore

	// RSI值
	tf.RSI3m = btcData.CurrentRSI7
	// 获取4h数据来获取RSI14和MACD
	btcData4h, err4h := market.GetWithTimeframe("BTCUSDT", "4h", 1000)
	if err4h == nil && btcData4h != nil && btcData4h.IntradaySeries != nil {
		if len(btcData4h.IntradaySeries.RSI14Values) > 0 {
			tf.RSI4h = btcData4h.IntradaySeries.RSI14Values[len(btcData4h.IntradaySeries.RSI14Values)-1]
		}
		if len(btcData4h.IntradaySeries.MACDValues) > 0 {
			tf.MACD4h = btcData4h.IntradaySeries.MACDValues[len(btcData4h.IntradaySeries.MACDValues)-1]
		}
	}

	// MACD值
	tf.MACD3m = btcData.CurrentMACD

	return tf
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

