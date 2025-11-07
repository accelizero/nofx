package trader

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"
	"backend/pkg/logger"
	"backend/pkg/storage"
)

// analyzePerformanceFromDB 从数据库记录分析历史表现
func (at *AutoTrader) analyzePerformanceFromDB(records []*storage.DecisionRecord) *logger.PerformanceAnalysis {
	analysis := &logger.PerformanceAnalysis{
		RecentTrades: []logger.TradeOutcome{},
		SymbolStats:  make(map[string]*logger.SymbolPerformance),
	}

	// 优先从交易记录数据库获取历史表现（更准确）
	if at.storageAdapter != nil {
		tradeStorage := at.storageAdapter.GetTradeStorage()
		if tradeStorage != nil {
			// 获取最近100笔交易
			trades, err := tradeStorage.GetLatestTrades(100)
			if err == nil && len(trades) > 0 {
				return at.analyzePerformanceFromTrades(trades)
			}
		}
	}

	// 如果交易记录不可用，从决策记录分析（较复杂，但作为备选）
	if len(records) == 0 {
		return analysis
	}

	// 改进：追踪持仓状态，使用更精确的匹配方法
	// 使用map[tradeID]map[string]interface{}而不是简单的FIFO
	openPositions := make(map[string]map[string]interface{})

	// 遍历决策记录，提取交易结果
	for _, record := range records {
		// 解析decisions字段
		var decisions []logger.DecisionAction
		if err := json.Unmarshal(record.Decisions, &decisions); err != nil {
			continue
		}

		for _, action := range decisions {
			if !action.Success {
				continue
			}

			symbol := action.Symbol
			side := ""
			if action.Action == "open_long" || action.Action == "close_long" {
				side = "long"
			} else if action.Action == "open_short" || action.Action == "close_short" {
				side = "short"
			}
			switch action.Action {
			case "open_long", "open_short":
				// 使用更 descriptive 的唯一标识符
				tradeID := fmt.Sprintf("%s_%s_%d_%d", symbol, side, record.CycleNumber, action.OrderID)
				if action.OrderID == 0 {
					// 如果没有订单ID，使用时间戳作为唯一标识
					tradeID = fmt.Sprintf("%s_%s_%d_%d", symbol, side, record.CycleNumber, action.Timestamp.Unix())
				}
				
				// 添加开仓记录到map
				openPositions[tradeID] = map[string]interface{}{
					"side":      side,
					"openPrice": action.Price,
					"openTime":  action.Timestamp,
					"quantity":  action.Quantity,
					"leverage":  action.Leverage,
					"symbol":    symbol,
					"cycleNum":  record.CycleNumber,
				}

			case "close_long", "close_short":
				// 改进：使用更智能的匹配策略
				// 首先尝试找到精确匹配的持仓（订单ID或时间戳匹配）
				var matchedTradeID string
				var matchedOpenPos map[string]interface{}
				
				// 遍历所有持仓，寻找最匹配的开仓记录
				for tradeID, openPos := range openPositions {
					if openPos["symbol"].(string) == symbol && openPos["side"].(string) == side {
						// 精确匹配，选择最早开仓的（FIFO）
						if matchedOpenPos == nil || openPos["openTime"].(time.Time).Before(matchedOpenPos["openTime"].(time.Time)) {
							matchedTradeID = tradeID
							matchedOpenPos = openPos
						}
					}
				}

				if matchedOpenPos == nil {
					continue
				}

				// 移除已匹配的开仓记录
				delete(openPositions, matchedTradeID)

				// 提取开仓信息
				openPrice, _ := matchedOpenPos["openPrice"].(float64)
				openTime, _ := matchedOpenPos["openTime"].(time.Time)
				quantity, _ := matchedOpenPos["quantity"].(float64)
				leverage, _ := matchedOpenPos["leverage"].(int)

				// 计算盈亏
				var pnl float64
				if side == "long" {
					pnl = quantity * (action.Price - openPrice)
				} else {
					pnl = quantity * (openPrice - action.Price)
				}

				// 计算盈亏百分比
				positionValue := quantity * openPrice
				marginUsed := positionValue / float64(leverage)
				pnlPct := 0.0
				if marginUsed > 0 {
					pnlPct = (pnl / marginUsed) * 100
				}

				// 记录交易结果
				outcome := logger.TradeOutcome{
					Symbol:        symbol,
					Side:          side,
					Quantity:      quantity,
					Leverage:      leverage,
					OpenPrice:     openPrice,
					ClosePrice:    action.Price,
					PositionValue: positionValue,
					MarginUsed:    marginUsed,
					PnL:           pnl,
					PnLPct:        pnlPct,
					Duration:      action.Timestamp.Sub(openTime).String(),
					OpenTime:      openTime,
					CloseTime:     action.Timestamp,
					WasStopLoss:   action.IsForced && pnl < 0,
					CloseReason:   "", // 从DecisionRecord构建时，CloseReason需要从其他地方获取
				}

				analysis.RecentTrades = append(analysis.RecentTrades, outcome)
				analysis.TotalTrades++

				// 分类交易
				if pnl > 0 {
					analysis.WinningTrades++
					analysis.AvgWin += pnl
				} else if pnl < 0 {
					analysis.LosingTrades++
					analysis.AvgLoss += pnl
				}

				// 更新币种统计
				if _, exists := analysis.SymbolStats[symbol]; !exists {
					analysis.SymbolStats[symbol] = &logger.SymbolPerformance{
						Symbol: symbol,
					}
				}
				stats := analysis.SymbolStats[symbol]
				stats.TotalTrades++
				stats.TotalPnL += pnl
				if pnl > 0 {
					stats.WinningTrades++
				} else if pnl < 0 {
					stats.LosingTrades++
				}
			}
		}
	}

	// 处理未平仓的记录警告
	if len(openPositions) > 0 {
		log.Printf("⚠️  警告：分析期间发现 %d 个未平仓的持仓记录，可能影响分析准确性", len(openPositions))
	}

	// 计算统计指标
	if analysis.TotalTrades > 0 {
		analysis.WinRate = (float64(analysis.WinningTrades) / float64(analysis.TotalTrades)) * 100

		totalWinAmount := analysis.AvgWin
		totalLossAmount := analysis.AvgLoss

		if analysis.WinningTrades > 0 {
			analysis.AvgWin /= float64(analysis.WinningTrades)
		}
		if analysis.LosingTrades > 0 {
			analysis.AvgLoss /= float64(analysis.LosingTrades)
		}

		// Profit Factor
		if totalLossAmount != 0 {
			analysis.ProfitFactor = totalWinAmount / (-totalLossAmount)
		} else if totalWinAmount > 0 {
			analysis.ProfitFactor = 999.0
		}
	}

	// 计算各币种胜率和平均盈亏
	bestPnL := -999999.0
	worstPnL := 999999.0
	for symbol, stats := range analysis.SymbolStats {
		if stats.TotalTrades > 0 {
			stats.WinRate = (float64(stats.WinningTrades) / float64(stats.TotalTrades)) * 100
			stats.AvgPnL = stats.TotalPnL / float64(stats.TotalTrades)

			if stats.TotalPnL > bestPnL {
				bestPnL = stats.TotalPnL
				analysis.BestSymbol = symbol
			}
			if stats.TotalPnL < worstPnL {
				worstPnL = stats.TotalPnL
				analysis.WorstSymbol = symbol
			}
		}
	}

	// 计算夏普比率（使用历史交易盈亏率）
	analysis.SharpeRatio = calculateSharpeRatio(analysis.RecentTrades)

	// 反转数组，让最新的在前
	for i, j := 0, len(analysis.RecentTrades)-1; i < j; i, j = i+1, j-1 {
		analysis.RecentTrades[i], analysis.RecentTrades[j] = analysis.RecentTrades[j], analysis.RecentTrades[i]
	}

	return analysis
}

// analyzePerformanceFromTrades 从交易记录分析历史表现（更准确）
func (at *AutoTrader) analyzePerformanceFromTrades(trades []*storage.TradeRecord) *logger.PerformanceAnalysis {
	analysis := &logger.PerformanceAnalysis{
		RecentTrades: []logger.TradeOutcome{},
		SymbolStats:  make(map[string]*logger.SymbolPerformance),
	}

	for _, trade := range trades {
		// 数据验证：确保关键字段有效
		if trade.Symbol == "" || trade.Side == "" {
			log.Printf("⚠️  跳过无效交易记录：缺少币种或方向信息")
			continue
		}
		// 只处理已平仓的交易（未平仓的记录CloseTime为nil）
		if trade.CloseTime == nil {
			continue // 跳过未平仓的交易
		}
		if trade.OpenPrice <= 0 || trade.ClosePrice <= 0 {
			log.Printf("⚠️  跳过无效交易记录 %s: 开仓价 %.4f 或平仓价 %.4f 无效", trade.Symbol, trade.OpenPrice, trade.ClosePrice)
			continue
		}
		if trade.OpenQuantity <= 0 {
			log.Printf("⚠️  跳过无效交易记录 %s: 开仓数量 %.4f 无效", trade.Symbol, trade.OpenQuantity)
			continue
		}

		// 转换为TradeOutcome
		// 重新计算持仓时长，避免使用数据库中可能错误的Duration字段
		var duration time.Duration
		if trade.CloseTime != nil {
			duration = trade.CloseTime.Sub(trade.OpenTime)
		}
		
		// 按照优先级获取平仓逻辑：
		// 1. close_logic - 直接平仓理由（AI决策close_long/close_short）
		// 2. update_sl_logic - 如果平仓是由update_sl挂单成交触发的（was_stop_loss=true且有update_sl_logic）
		// 3. forced_close_logic - 强制平仓理由
		// 4. exit_logic - 建仓时记录的出场逻辑
		closeReason := ""
		if trade.CloseLogic != "" {
			closeReason = trade.CloseLogic // 优先使用直接平仓的理由
		} else if trade.WasStopLoss && trade.UpdateSLLogic != "" {
			// 如果是由update_sl挂单成交的（was_stop_loss=true且有update_sl_logic），使用update_sl_logic
			closeReason = trade.UpdateSLLogic
		} else if trade.ForcedCloseLogic != "" {
			closeReason = trade.ForcedCloseLogic // 其次是强制平仓的理由
		} else if trade.ExitLogic != "" {
			closeReason = trade.ExitLogic // 然后是进场时规划的出场逻辑
		} else if trade.CloseReason != "" {
			closeReason = trade.CloseReason // 最后使用旧的CloseReason字段（向后兼容）
		} else {
			closeReason = "未提供平仓逻辑" // 默认理由
		}
		
		var closeTime time.Time
		if trade.CloseTime != nil {
			closeTime = *trade.CloseTime
		}
		
		outcome := logger.TradeOutcome{
			Symbol:        trade.Symbol,
			Side:          trade.Side,
			Quantity:      trade.OpenQuantity,
			Leverage:      trade.OpenLeverage,
			OpenPrice:     trade.OpenPrice,
			ClosePrice:    trade.ClosePrice,
			PositionValue: trade.PositionValue,
			MarginUsed:    trade.MarginUsed,
			PnL:           trade.PnL,
			PnLPct:        trade.PnLPct,
			Duration:      duration.String(),
			OpenTime:      trade.OpenTime,
			CloseTime:     closeTime,
			WasStopLoss:   trade.WasStopLoss,
			CloseReason:   closeReason, // 使用优先级确定的平仓逻辑
		}

		analysis.RecentTrades = append(analysis.RecentTrades, outcome)
		analysis.TotalTrades++

		// 分类交易
		if trade.PnL > 0 {
			analysis.WinningTrades++
			analysis.AvgWin += trade.PnL
		} else if trade.PnL < 0 {
			analysis.LosingTrades++
			analysis.AvgLoss += trade.PnL
		}

		// 更新币种统计
		if _, exists := analysis.SymbolStats[trade.Symbol]; !exists {
			analysis.SymbolStats[trade.Symbol] = &logger.SymbolPerformance{
				Symbol: trade.Symbol,
			}
		}
		stats := analysis.SymbolStats[trade.Symbol]
		stats.TotalTrades++
		stats.TotalPnL += trade.PnL
		if trade.PnL > 0 {
			stats.WinningTrades++
		} else if trade.PnL < 0 {
			stats.LosingTrades++
		}
	}

	// 计算统计指标
	if analysis.TotalTrades > 0 {
		analysis.WinRate = (float64(analysis.WinningTrades) / float64(analysis.TotalTrades)) * 100

		totalWinAmount := analysis.AvgWin
		totalLossAmount := analysis.AvgLoss

		if analysis.WinningTrades > 0 {
			analysis.AvgWin /= float64(analysis.WinningTrades)
		}
		if analysis.LosingTrades > 0 {
			analysis.AvgLoss /= float64(analysis.LosingTrades)
		}

		// Profit Factor
		if totalLossAmount != 0 {
			analysis.ProfitFactor = totalWinAmount / (-totalLossAmount)
		} else if totalWinAmount > 0 {
			analysis.ProfitFactor = 999.0
		}
	}

	// 计算各币种胜率和平均盈亏
	bestPnL := -999999.0
	worstPnL := 999999.0
	for symbol, stats := range analysis.SymbolStats {
		if stats.TotalTrades > 0 {
			stats.WinRate = (float64(stats.WinningTrades) / float64(stats.TotalTrades)) * 100
			stats.AvgPnL = stats.TotalPnL / float64(stats.TotalTrades)

			if stats.TotalPnL > bestPnL {
				bestPnL = stats.TotalPnL
				analysis.BestSymbol = symbol
			}
			if stats.TotalPnL < worstPnL {
				worstPnL = stats.TotalPnL
				analysis.WorstSymbol = symbol
			}
		}
	}

	// 计算夏普比率（使用历史交易盈亏率）
	analysis.SharpeRatio = calculateSharpeRatio(analysis.RecentTrades)

	// 反转数组，让最新的在前
	for i, j := 0, len(analysis.RecentTrades)-1; i < j; i, j = i+1, j-1 {
		analysis.RecentTrades[i], analysis.RecentTrades[j] = analysis.RecentTrades[j], analysis.RecentTrades[i]
	}

	return analysis
}


// calculateSharpeRatio 计算夏普比率
// 使用历史交易的盈亏百分比来计算
func calculateSharpeRatio(recentTrades []logger.TradeOutcome) float64 {
	if len(recentTrades) < 2 {
		return 0.0 // 需要至少2笔交易才能计算夏普比率
	}

	// 计算所有交易的盈亏百分比均值
	var sum float64
	for _, trade := range recentTrades {
		sum += trade.PnLPct
	}
	mean := sum / float64(len(recentTrades))

	// 计算标准差
	var variance float64
	for _, trade := range recentTrades {
		deviation := trade.PnLPct - mean
		variance += deviation * deviation
	}
	variance /= float64(len(recentTrades))
	stdDev := math.Sqrt(variance)

	// 如果标准差为0，返回0（无风险或无收益变化）
	if stdDev == 0 {
		return 0.0
	}

	// 夏普比率 = (收益率均值 - 无风险收益率) / 收益率标准差
	// 这里简化为收益率均值 / 标准差 (假设无风险收益率为0)
	return mean / stdDev
}
