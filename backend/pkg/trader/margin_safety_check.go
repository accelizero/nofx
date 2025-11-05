package trader

import (
	"fmt"
	"log"
	"backend/pkg/decision"
	"backend/pkg/market"
)

// checkMarginAndBalanceSafety 检查保证金和余额安全性（开仓前检查）
func (at *AutoTrader) checkMarginAndBalanceSafety(ctx *decision.Context, decision *decision.Decision) error {
	// 1. 获取当前价格
	marketData, err := market.Get(decision.Symbol)
	if err != nil {
		return fmt.Errorf("获取市场数据失败: %w", err)
	}
	
	if marketData.CurrentPrice <= 0 {
		return fmt.Errorf("当前价格无效: %.4f", marketData.CurrentPrice)
	}
	
	// 2. 计算新仓位需要的保证金
	positionValue := decision.PositionSizeUSD
	marginRequired := positionValue / float64(decision.Leverage)
	
	// 3. 计算开仓后的总保证金使用率
	currentMarginUsed := ctx.Account.MarginUsed
	totalMarginAfterOpen := currentMarginUsed + marginRequired
	totalMarginUsedPct := 0.0
	if ctx.Account.TotalEquity > 0 {
		totalMarginUsedPct = (totalMarginAfterOpen / ctx.Account.TotalEquity) * 100
	}
	
	// 3.5. 判断是否为单个币种交易
	// 如果当前没有持仓，开仓后只有一个币种
	// 如果当前有持仓，检查是否与要开的仓是同一个币种
	isSingleSymbol := false
	if ctx.Account.PositionCount == 0 {
		// 当前没有持仓，开仓后只有一个币种
		isSingleSymbol = true
	} else {
		// 检查当前持仓是否只有同一个币种
		symbolSet := make(map[string]bool)
		for _, pos := range ctx.Positions {
			symbolSet[pos.Symbol] = true
		}
		// 如果当前持仓只有这个币种，或者没有持仓（开仓后只有这个币种）
		if len(symbolSet) == 1 {
			for symbol := range symbolSet {
				if symbol == decision.Symbol {
					isSingleSymbol = true
					break
				}
			}
		}
	}
	
	// 4. 根据币种数量选择保证金使用率限制
	maxMarginUsagePct := MaxMarginUsagePct
	if isSingleSymbol {
		maxMarginUsagePct = MaxMarginUsagePctSingleSymbol
		log.Printf("  ℹ️  单币种交易模式: 保证金使用率限制为 %.0f%%", maxMarginUsagePct)
	}
	
	// 检查保证金使用率是否超过限制
	if totalMarginUsedPct > maxMarginUsagePct {
		return fmt.Errorf("❌ 保证金使用率超限: 开仓后预计使用%.1f%% > %.0f%%限制 (当前%.1f%% + 新仓位%.1f%% = %.1f%%)", 
			totalMarginUsedPct, maxMarginUsagePct, 
			(currentMarginUsed/ctx.Account.TotalEquity)*100, 
			(marginRequired/ctx.Account.TotalEquity)*100, 
			totalMarginUsedPct)
	}
	
	// 5. 检查可用余额是否足够
	// 需要额外保留一些余额作为缓冲（至少保留总净值的MinReserveBalancePct%）
	minReserveBalance := ctx.Account.TotalEquity * (MinReserveBalancePct / 100.0)
	availableBalanceAfterMargin := ctx.Account.AvailableBalance - marginRequired
	
	if availableBalanceAfterMargin < minReserveBalance {
		return fmt.Errorf("❌ 可用余额不足: 开仓需要保证金%.2f USDT，剩余%.2f < 最小保留%.2f (总净值5%%)", 
			marginRequired, availableBalanceAfterMargin, minReserveBalance)
	}
	
	// 6. 预估强制平仓价格并检查是否过高（太接近当前价格）
	// 强制平仓价格计算：
	// 做多: liquidationPrice = entryPrice * (1 - 1/leverage)
	// 做空: liquidationPrice = entryPrice * (1 + 1/leverage)
	// 但实际计算需要考虑所有持仓的综合保证金
	// 简化：检查如果这个仓位亏损到强制平仓，价格距离是否合理
	
	estimatedEntryPrice := marketData.CurrentPrice
	var estimatedLiquidationPrice float64
	var priceDistancePct float64
	
	if decision.Action == "open_long" {
		// 做多：强制平仓价格在下方
		// 公式：liquidationPrice = entryPrice * (1 - (1/leverage + maintenanceMarginRate))
		// 例如：20x杠杆，维持保证金1%，价格距离 = 1/20 + 0.01 = 6%
		marginRate := 1.0/float64(decision.Leverage) + MaintenanceMarginRate
		estimatedLiquidationPrice = estimatedEntryPrice * (1 - marginRate)
		priceDistancePct = ((estimatedEntryPrice - estimatedLiquidationPrice) / estimatedEntryPrice) * 100
	} else {
		// 做空：强制平仓价格在上方
		marginRate := 1.0/float64(decision.Leverage) + MaintenanceMarginRate
		estimatedLiquidationPrice = estimatedEntryPrice * (1 + marginRate)
		priceDistancePct = ((estimatedLiquidationPrice - estimatedEntryPrice) / estimatedEntryPrice) * 100
	}
	
	// 检查强制平仓价格距离是否过近
	if priceDistancePct < MinSafeDistancePct {
		return fmt.Errorf("❌ 强制平仓价格过近: 预估强制平仓价%.4f距离当前价%.4f仅%.2f%% < %.1f%%安全距离 (杠杆%dx过高，风险极高，可能导致爆仓)",
			estimatedLiquidationPrice, estimatedEntryPrice, priceDistancePct, MinSafeDistancePct, decision.Leverage)
	}
	
	// 7. 检查止损价是否比强制平仓价更安全
	// 如果止损价距离强制平仓价太近（< 2%），也很危险
	if decision.StopLoss > 0 {
		var stopLossDistancePct float64
		if decision.Action == "open_long" {
			if decision.StopLoss >= estimatedEntryPrice {
				return fmt.Errorf("❌ 止损价设置错误: 做多时止损价%.4f应该小于入场价%.4f", decision.StopLoss, estimatedEntryPrice)
			}
			stopLossDistancePct = ((estimatedEntryPrice - decision.StopLoss) / estimatedEntryPrice) * 100
			
			// 检查止损价是否比强制平仓价安全
			if decision.StopLoss <= estimatedLiquidationPrice {
				return fmt.Errorf("❌ 止损价过于接近强制平仓价: 止损价%.4f <= 强制平仓价%.4f (距离仅%.2f%%)，风险极高", 
					decision.StopLoss, estimatedLiquidationPrice, stopLossDistancePct)
			}
		} else {
			if decision.StopLoss <= estimatedEntryPrice {
				return fmt.Errorf("❌ 止损价设置错误: 做空时止损价%.4f应该大于入场价%.4f", decision.StopLoss, estimatedEntryPrice)
			}
			stopLossDistancePct = ((decision.StopLoss - estimatedEntryPrice) / estimatedEntryPrice) * 100
			
			// 检查止损价是否比强制平仓价安全
			if decision.StopLoss >= estimatedLiquidationPrice {
				return fmt.Errorf("❌ 止损价过于接近强制平仓价: 止损价%.4f >= 强制平仓价%.4f (距离仅%.2f%%)，风险极高", 
					decision.StopLoss, estimatedLiquidationPrice, stopLossDistancePct)
			}
		}
	}
	
	// 所有检查通过
	log.Printf("  ✓ 风控检查通过: 保证金使用率%.1f%% < %.0f%%, 可用余额充足, 强制平仓价安全距离%.2f%%", 
		totalMarginUsedPct, maxMarginUsagePct, priceDistancePct)
	
	return nil
}

