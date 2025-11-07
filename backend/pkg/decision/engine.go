package decision

import (
	"encoding/json"
	"fmt"
	"log"
	"backend/pkg/config"
	"backend/pkg/logger"
	"backend/pkg/market"
	"backend/pkg/mcp"
	"strings"
	"time"
)

// PositionInfo 持仓信息
type PositionInfo struct {
	Symbol           string         `json:"symbol"`
	Side             string         `json:"side"` // "long" or "short"
	EntryPrice       float64        `json:"entry_price"`
	MarkPrice        float64        `json:"mark_price"`
	Quantity         float64        `json:"quantity"`
	Leverage         int            `json:"leverage"`
	UnrealizedPnL    float64        `json:"unrealized_pnl"`
	UnrealizedPnLPct float64        `json:"unrealized_pnl_pct"`
	LiquidationPrice float64        `json:"liquidation_price"`
	MarginUsed       float64        `json:"margin_used"`
	UpdateTime       int64          `json:"update_time"` // 持仓更新时间戳（毫秒）
	StopLoss         float64        `json:"stop_loss,omitempty"` // 当前设置的止损价格（如果有）
	TakeProfit       float64        `json:"take_profit,omitempty"` // 当前设置的止盈价格（如果有）
	EntryLogic       *EntryLogic    `json:"entry_logic,omitempty"` // 进场逻辑
	ExitLogic        *ExitLogic     `json:"exit_logic,omitempty"`  // 出场逻辑
	LogicInvalid     bool           `json:"logic_invalid,omitempty"` // 逻辑是否失效
	InvalidReasons   []string       `json:"invalid_reasons,omitempty"` // 失效原因列表
}

// AccountInfo 账户信息
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`      // 账户净值
	AvailableBalance float64 `json:"available_balance"` // 可用余额
	TotalPnL         float64 `json:"total_pnl"`         // 总盈亏
	TotalPnLPct      float64 `json:"total_pnl_pct"`     // 总盈亏百分比
	MarginUsed       float64 `json:"margin_used"`       // 已用保证金
	MarginUsedPct    float64 `json:"margin_used_pct"`   // 保证金使用率
	PositionCount    int     `json:"position_count"`    // 持仓数量
}

// CandidateCoin 候选币种（来自币种池）
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"` // 币种来源
}

// Context 交易上下文（传递给AI的完整信息）
type Context struct {
	CurrentTime        string                  `json:"current_time"`
	RuntimeMinutes     int                     `json:"runtime_minutes"`
	CallCount          int                     `json:"call_count"`
	Account            AccountInfo             `json:"account"`
	Positions          []PositionInfo          `json:"positions"`
	CandidateCoins     []CandidateCoin         `json:"candidate_coins"`
	MarketDataMap      map[string]*market.Data `json:"-"` // 不序列化，但内部使用
	Performance        interface{}             `json:"-"` // 历史表现分析（logger.PerformanceAnalysis）
	RecentForcedCloses []string                `json:"-"` // 最近的强制平仓记录（用于AI参考）
	BTCETHLeverage     int                     `json:"-"` // BTC/ETH杠杆倍数（从配置读取）
	AltcoinLeverage    int                     `json:"-"` // 山寨币杠杆倍数（从配置读取）
	SkipLiquidityCheck  bool                    `json:"-"` // 是否跳过流动性检查（从配置读取）
	AnalysisMode       string                  `json:"-"` // 分析模式（固定为"multi_timeframe"）
	MultiTimeframeConfig *config.MultiTimeframeConfig `json:"-"` // 多时间框架配置
	StrategyName string `json:"-"` // 策略名称（从配置读取）
}

// Decision AI的交易决策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"` // "open_long", "open_short", "close_long", "close_short", "hold", "wait"
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	Confidence      int     `json:"confidence,omitempty"` // 信心度 (0-100)
	RiskUSD         float64 `json:"risk_usd,omitempty"`   // 最大美元风险
	Reasoning       string  `json:"reasoning"`            // 进场逻辑（开仓时）或平仓理由（平仓时）
	ExitReasoning   string  `json:"exit_reasoning,omitempty"` // 出场逻辑规划（仅在开仓时提供）
}

// FullDecision AI的完整决策（包含思维链）
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"` // 发送给AI的输入prompt
	CoTTrace   string     `json:"cot_trace"`   // 思维链分析（AI输出）
	Decisions  []Decision `json:"decisions"`   // 具体决策列表
	Timestamp  time.Time  `json:"timestamp"`
}

// GetFullDecision 获取AI的完整交易决策（批量分析所有币种和持仓）
// 使用多时间框架分析模式
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 1. 为所有币种获取市场数据
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 2. 使用多时间框架分析模式构建prompt
	log.Printf("📊 使用多时间框架分析模式")
	userPrompt, err := buildMultiTimeframePrompt(ctx, mcpClient)
	if err != nil {
		return nil, fmt.Errorf("构建多时间框架prompt失败: %w", err)
	}

	// 3. 构建 System Prompt（固定规则）和 User Prompt（动态数据）
	// 判断是否只交易一个币种
	isSingleSymbol := len(ctx.Positions) == 0 || func() bool {
		symbolSet := make(map[string]bool)
		for _, pos := range ctx.Positions {
			symbolSet[pos.Symbol] = true
		}
		return len(symbolSet) == 1
	}()
	systemPrompt := buildSystemPrompt(ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage, isSingleSymbol, ctx.StrategyName)

	// 4. 调用AI API（使用 system + user prompt）
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI API失败: %w", err)
	}

	// 5. 解析AI响应
	decision, err := parseFullDecisionResponse(aiResponse, ctx.Account.TotalEquity, ctx.BTCETHLeverage, ctx.AltcoinLeverage)
	if err != nil {
		return nil, fmt.Errorf("解析AI响应失败: %w", err)
	}

	decision.Timestamp = time.Now()
	decision.UserPrompt = userPrompt // 保存输入prompt
	return decision, nil
}

// fetchMarketDataForContext 为上下文中的所有币种获取市场数据
func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)

	// 收集所有需要获取数据的币种
	symbolSet := make(map[string]bool)

	// 1. 优先获取持仓币种的数据（这是必须的）
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	// 2. 候选币种数量根据账户状态动态调整
	maxCandidates := calculateMaxCandidates(ctx)
	for i, coin := range ctx.CandidateCoins {
		if i >= maxCandidates {
			break
		}
		symbolSet[coin.Symbol] = true
	}

	// 统计信息
	totalSymbols := len(symbolSet)
	if totalSymbols == 0 {
		log.Printf("📋 候选币种列表为空，无需获取市场数据")
		return nil
	}

	log.Printf("📊 开始获取 %d 个币种的市场数据（持仓: %d, 候选: %d）",
		totalSymbols, len(ctx.Positions), len(ctx.CandidateCoins))

	// 持仓币种集合（用于判断是否跳过OI检查）
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	// 统计变量
	successCount := 0
	failedCount := 0
	filteredCount := 0
	failedReasons := make(map[string]string)
	filteredReasons := make(map[string]string)

	// 逐个处理币种
	for symbol := range symbolSet {
		isExistingPosition := positionSymbols[symbol]
		log.Printf("  🔍 处理币种: %s (持仓: %v)", symbol, isExistingPosition)

		// 获取市场数据
		data, err := market.Get(symbol)
		if err != nil {
			failedCount++
			failedReasons[symbol] = fmt.Sprintf("获取市场数据失败: %v", err)
			log.Printf("    ❌ %s: 获取市场数据失败 - %v", symbol, err)
			continue
		}

		// 检查必要的数据字段
		if data == nil {
			failedCount++
			failedReasons[symbol] = "市场数据为空"
			log.Printf("    ❌ %s: 市场数据为空", symbol)
			continue
		}

		// 对于新候选币种（非持仓），进行流动性过滤（如果配置允许）
		if !isExistingPosition {
			// 检查价格有效性（这个检查始终执行，不管是否跳过流动性检查）
			if data.CurrentPrice <= 0 {
				filteredCount++
				filteredReasons[symbol] = fmt.Sprintf("当前价格为0或无效: %.4f", data.CurrentPrice)
				log.Printf("    ⚠️  %s: 当前价格为0或无效(%.4f)，跳过此币种", symbol, data.CurrentPrice)
				continue
			}

			// 如果配置了跳过流动性检查，则跳过OI检查
			if ctx.SkipLiquidityCheck {
				log.Printf("    ✓ %s: 跳过流动性检查（配置已启用skip_liquidity_check）", symbol)
			} else {
				// 执行流动性检查
				// 检查持仓量数据
				if data.OpenInterest == nil {
					filteredCount++
					filteredReasons[symbol] = "持仓量(OI)数据为空"
					log.Printf("    ⚠️  %s: 持仓量(OI)数据为空，跳过此币种", symbol)
					continue
				}

				// 计算持仓价值（USD）= 持仓量 × 当前价格
				oiValue := data.OpenInterest.Latest * data.CurrentPrice
				oiValueInMillions := oiValue / 1_000_000 // 转换为百万美元单位

				// 流动性过滤：持仓价值低于15M USD的币种不做
				if oiValueInMillions < 15 {
					filteredCount++
					filteredReasons[symbol] = fmt.Sprintf("持仓价值过低: %.2fM USD < 15M", oiValueInMillions)
					log.Printf("    ⚠️  %s: 持仓价值过低(%.2fM USD < 15M)，跳过此币种 [持仓量:%.0f × 价格:%.4f]",
						symbol, oiValueInMillions, data.OpenInterest.Latest, data.CurrentPrice)
					continue
				}

				log.Printf("    ✓ %s: 通过流动性检查 [持仓价值: %.2fM USD, 价格: %.4f]",
					symbol, oiValueInMillions, data.CurrentPrice)
			}
		} else {
			log.Printf("    ✓ %s: 持仓币种，跳过流动性检查", symbol)
		}

		// 成功获取并验证通过，添加到市场数据映射
		ctx.MarketDataMap[symbol] = data
		successCount++
	}

	// 输出统计总结
	log.Printf("\n📊 市场数据获取完成:")
	log.Printf("  • 总计: %d 个币种", totalSymbols)
	log.Printf("  • 成功: %d 个币种（将发送给AI）", successCount)
	if failedCount > 0 {
		log.Printf("  • 失败: %d 个币种", failedCount)
		for symbol, reason := range failedReasons {
			log.Printf("    - %s: %s", symbol, reason)
		}
	}
	if filteredCount > 0 {
		log.Printf("  • 过滤: %d 个币种（不达标）", filteredCount)
		for symbol, reason := range filteredReasons {
			log.Printf("    - %s: %s", symbol, reason)
		}
	}

	if successCount == 0 {
		log.Printf("\n⚠️  警告: 没有任何币种通过验证，AI将不会收到任何候选币种数据")
	}

	return nil
}

// calculateMaxCandidates 根据账户状态计算需要分析的候选币种数量
func calculateMaxCandidates(ctx *Context) int {
	// 直接返回候选池的全部币种数量
	// 因为候选池已经在 auto_trader.go 中筛选过了
	return len(ctx.CandidateCoins)
}

// buildSystemPrompt 构建 System Prompt（固定规则，可缓存）
func buildSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int, isSingleSymbol bool, strategyName string) string {
	// 验证策略名称
	if strategyName == "" {
		log.Printf("⚠️  策略名称为空，使用默认策略 'base_prompt'")
		strategyName = "base_prompt"
	}
	
	// 加载策略提示词
	log.Printf("📋 加载策略提示词: 策略='%s'", strategyName)
	strategyPrompt, err := LoadStrategyPrompt(strategyName)
	if err != nil {
		log.Printf("⚠️  加载策略提示词失败，使用默认提示词: %v", err)
		// 如果加载失败，使用默认提示词（保持向后兼容）
		return buildDefaultSystemPrompt(accountEquity, btcEthLeverage, altcoinLeverage, isSingleSymbol)
	}
	
	log.Printf("✅ 策略提示词加载成功: '%s' (长度: %d 字符)", strategyName, len(strategyPrompt))
	
	var sb strings.Builder
	sb.WriteString(strategyPrompt)
	sb.WriteString("\n\n")
	
	// 添加动态仓位信息（这部分需要根据账户状态动态生成）
	sb.WriteString("# 💰 仓位配置（动态）\n\n")
	if isSingleSymbol {
		// 单币种交易：仓位应该打满，目标保证金使用率50%
		sb.WriteString(fmt.Sprintf("**单币仓位（单币种模式）**: \n"))
		sb.WriteString(fmt.Sprintf("- ⚠️ **重要**：当前只交易一个币种，应该使用更大的仓位\n"))
		sb.WriteString(fmt.Sprintf("- BTC/ETH 推荐仓位: %.0f USDT (目标保证金使用率50%%)\n", accountEquity*0.5*float64(btcEthLeverage)))
		sb.WriteString(fmt.Sprintf("   - 计算公式: position_size_usd = (账户净值 * 0.5) * 杠杆 = %.0f * 0.5 * %d = %.0f\n", accountEquity, btcEthLeverage, accountEquity*0.5*float64(btcEthLeverage)))
		sb.WriteString(fmt.Sprintf("- 山寨币推荐仓位: %.0f USDT (目标保证金使用率50%%)\n", accountEquity*0.5*float64(altcoinLeverage)))
		sb.WriteString(fmt.Sprintf("   - 不要保守，应该尽量打满仓位到50%%保证金使用率\n"))
		sb.WriteString("**保证金**: 单币种时使用率 ≤ 50%\n\n")
	} else {
		sb.WriteString(fmt.Sprintf("**单币仓位**: 山寨%.0f-%.0f U(%dx杠杆) | BTC/ETH %.0f-%.0f U(%dx杠杆)\n",
			accountEquity*0.8*float64(altcoinLeverage), accountEquity*1.5*float64(altcoinLeverage), altcoinLeverage, 
			accountEquity*5*float64(btcEthLeverage), accountEquity*10*float64(btcEthLeverage), btcEthLeverage))
		sb.WriteString(fmt.Sprintf("   - ⚠️ **重要**：BTC/ETH仓位价值绝对上限为账户净值×%.1f倍（当前%.0f USDT），山寨币为账户净值×%.1f倍（当前%.0f USDT）\n", 
			float64(btcEthLeverage)*0.9, accountEquity*float64(btcEthLeverage)*0.9, 
			float64(altcoinLeverage)*0.9, accountEquity*float64(altcoinLeverage)*0.9))
		sb.WriteString("**保证金**: 总使用率 ≤ 90%（多币种模式）\n\n")
	}

	return sb.String()
}

// buildDefaultSystemPrompt 构建默认系统提示词（向后兼容，当策略文件加载失败时使用）
func buildDefaultSystemPrompt(accountEquity float64, btcEthLeverage, altcoinLeverage int, isSingleSymbol bool) string {
	// 这里保留原来的完整提示词逻辑作为fallback
	// 为了简化，我们直接返回一个基本提示词，建议用户修复策略文件
	return "⚠️ 警告：策略文件加载失败，请检查配置。使用默认提示词。\n\n" +
		"你是专业的加密货币交易AI，在币安合约市场进行自主交易。\n\n" +
		"请遵循风险控制和交易规则进行交易。"
}

// buildMultiTimeframePrompt 构建多时间框架分析的prompt（使用新的分析器）
func buildMultiTimeframePrompt(ctx *Context, mcpClient *mcp.Client) (string, error) {
	// 创建多时间框架分析器
	analyzer := NewMultiTimeframeAnalyzer(ctx.MultiTimeframeConfig)
	
	// 执行分析
	result, err := analyzer.Analyze(ctx)
	if err != nil {
		return "", fmt.Errorf("多时间框架分析失败: %w", err)
	}
	
	if len(result.SymbolScores) == 0 {
		return "", fmt.Errorf("多时间框架分析结果为空，无可用币种数据")
	}
	
	// 构建prompt
	var sb strings.Builder
	
	// 系统状态信息（先显示当前周期信息，让AI知道这是一个新的周期）
	sb.WriteString(fmt.Sprintf("**时间**: %s | **周期**: #%d | **运行**: %d分钟 | **模式**: 多时间框架分析\n\n",
		ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))
	
	// 账户状态
	availablePct := 0.0
	if ctx.Account.TotalEquity > 0 {
		availablePct = (ctx.Account.AvailableBalance / ctx.Account.TotalEquity) * 100
	}
	// 盈亏显示格式：盈亏=-1.08 (-0.59%)
	sb.WriteString(fmt.Sprintf("**账户**: 净值%.2f | 余额%.2f (%.1f%%) | 盈亏%.2f (%.2f%%) | 保证金%.1f%% | 持仓%d个\n\n",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, availablePct,
		ctx.Account.TotalPnL, ctx.Account.TotalPnLPct, ctx.Account.MarginUsedPct, ctx.Account.PositionCount))
	
	// 当前持仓 - 多时间框架分析
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 📊 当前持仓（多时间框架分析）\n\n")
		for i, pos := range ctx.Positions {
			holdingDuration := ""
			if pos.UpdateTime > 0 {
				durationMs := time.Now().UnixMilli() - pos.UpdateTime
				durationMin := durationMs / (1000 * 60)
				if durationMin < 60 {
					holdingDuration = fmt.Sprintf(" | 持仓时长%d分钟", durationMin)
				} else {
					durationHour := durationMin / 60
					durationMinRemainder := durationMin % 60
					holdingDuration = fmt.Sprintf(" | 持仓时长%d小时%d分钟", durationHour, durationMinRemainder)
				}
			}
			
			// 使用交易所API返回的未实现盈亏（最准确）
			// UnrealizedPnL是盈亏金额（USDT），UnrealizedPnLPct是盈亏百分比（杠杆后）
			// 格式：盈亏=-1.08 (-0.59%)
			sb.WriteString(fmt.Sprintf("%d. %s %s | 入场价%.4f 当前价%.4f | 杠杆%dx | 盈亏%.2f (%.2f%%) | 保证金%.0f | 强平价%.4f%s\n",
				i+1, pos.Symbol, strings.ToUpper(pos.Side),
				pos.EntryPrice, pos.MarkPrice, pos.Leverage, pos.UnrealizedPnL, pos.UnrealizedPnLPct,
				pos.MarginUsed, pos.LiquidationPrice, holdingDuration))
			
			// 注释掉评分信息，让AI自己判断
			// if score, exists := result.SymbolScores[pos.Symbol]; exists {
			// 	sb.WriteString(fmt.Sprintf("   **多时间框架评分**: 做多%.2f | 做空%.2f | 推荐方向:%s\n",
			// 		score.LongScore.WeightedScore, score.ShortScore.WeightedScore,
			// 		score.RecommendedDirection))
			// }
			sb.WriteString("\n")
			
			// 显示当前设置的止损/止盈价格（始终显示，让AI知道当前状态）
			sb.WriteString("**🛡️ 止损/止盈设置**:\n")
			if pos.StopLoss > 0 {
				sb.WriteString(fmt.Sprintf("- 止损价: %.4f", pos.StopLoss))
				if pos.Side == "long" {
					sb.WriteString(fmt.Sprintf(" (距离入场价: %.2f%%, 距离当前价: %.2f%%)\n", 
						((pos.EntryPrice-pos.StopLoss)/pos.EntryPrice)*100,
						((pos.MarkPrice-pos.StopLoss)/pos.MarkPrice)*100))
				} else {
					sb.WriteString(fmt.Sprintf(" (距离入场价: %.2f%%, 距离当前价: %.2f%%)\n", 
						((pos.StopLoss-pos.EntryPrice)/pos.EntryPrice)*100,
						((pos.StopLoss-pos.MarkPrice)/pos.MarkPrice)*100))
				}
			} else {
				sb.WriteString("- 止损价: 未设置\n")
			}
			if pos.TakeProfit > 0 {
				sb.WriteString(fmt.Sprintf("- 止盈价: %.4f", pos.TakeProfit))
				if pos.Side == "long" {
					sb.WriteString(fmt.Sprintf(" (距离入场价: +%.2f%%, 距离当前价: +%.2f%%)\n", 
						((pos.TakeProfit-pos.EntryPrice)/pos.EntryPrice)*100,
						((pos.TakeProfit-pos.MarkPrice)/pos.MarkPrice)*100))
				} else {
					sb.WriteString(fmt.Sprintf(" (距离入场价: +%.2f%%, 距离当前价: +%.2f%%)\n", 
						((pos.EntryPrice-pos.TakeProfit)/pos.EntryPrice)*100,
						((pos.MarkPrice-pos.TakeProfit)/pos.MarkPrice)*100))
				}
			} else {
				sb.WriteString("- 止盈价: 未设置\n")
			}
			sb.WriteString("\n")
			
			// 显示进场/出场逻辑和检查结果（无论是否有逻辑都显示，让AI了解情况）
			sb.WriteString("**📝 持仓逻辑**:\n\n")
			
			// 进场逻辑
			if pos.EntryLogic != nil {
				sb.WriteString("**进场逻辑**:\n")
				sb.WriteString(fmt.Sprintf("- 推理: %s\n", pos.EntryLogic.Reasoning))
				if pos.EntryLogic.MultiTimeframe != nil && pos.EntryLogic.MultiTimeframe.MajorTrend != "" {
					sb.WriteString(fmt.Sprintf("- 多时间框架: 主要趋势=%s\n", pos.EntryLogic.MultiTimeframe.MajorTrend))
				}
				if !pos.EntryLogic.Timestamp.IsZero() {
					sb.WriteString(fmt.Sprintf("- 记录时间: %s\n", pos.EntryLogic.Timestamp.Format("2006-01-02 15:04:05")))
				}
				sb.WriteString("\n")
			} else {
				sb.WriteString("**进场逻辑**: ⚠️ 未记录（该持仓没有明确的进场逻辑）\n\n")
			}
			
			// 出场逻辑
			if pos.ExitLogic != nil {
				sb.WriteString("**出场逻辑**:\n")
				sb.WriteString(fmt.Sprintf("- 规划: %s\n", pos.ExitLogic.Reasoning))
				if pos.ExitLogic.MultiTimeframe != nil && pos.ExitLogic.MultiTimeframe.MajorTrend != "" {
					sb.WriteString(fmt.Sprintf("- 多时间框架: 主要趋势=%s\n", pos.ExitLogic.MultiTimeframe.MajorTrend))
				}
				if !pos.ExitLogic.Timestamp.IsZero() {
					sb.WriteString(fmt.Sprintf("- 规划时间: %s\n", pos.ExitLogic.Timestamp.Format("2006-01-02 15:04:05")))
				}
				sb.WriteString("\n")
			} else {
				sb.WriteString("**出场逻辑**: ⚠️ 未规划（建议补全，明确出场条件）\n\n")
			}
		}
	} else {
		sb.WriteString("**当前持仓**: 无\n\n")
	}
	
	// 候选币种 - 按多时间框架评分排序
	sb.WriteString(fmt.Sprintf("## 🎯 候选币种（按多时间框架评分排序，共%d个）\n\n", len(result.SortedSymbols)))
	
	for i, symbol := range result.SortedSymbols {
		// 注释掉评分信息，让AI自己判断
		// score := result.SymbolScores[symbol]
		data := result.DataMap[symbol]
		
		sb.WriteString(fmt.Sprintf("### %d. %s\n\n", i+1, symbol))
		
		// 根据币种类型确定杠杆倍数
		leverage := ctx.AltcoinLeverage
		if symbol == "BTCUSDT" || symbol == "ETHUSDT" {
			leverage = ctx.BTCETHLeverage
		}
		sb.WriteString(fmt.Sprintf("**杠杆倍数**：%d\n\n", leverage))
		
		// 注释掉评分信息，让AI自己判断
		// sb.WriteString(fmt.Sprintf("**评分**: 做多%.2f | 做空%.2f | 推荐方向: **%s**\n\n",
		// 	score.LongScore.WeightedScore, score.ShortScore.WeightedScore,
		// 	strings.ToUpper(score.RecommendedDirection)))
		
		// 各时间框架详细数据（包含完整的序列数据：DIF、DEA、HIST、成交量等）
		sb.WriteString("**多时间框架数据**:\n\n")
		
		// 日线数据（完整序列）
		// if data.DailyData != nil {
		// 	sb.WriteString("**日线 (1d) 数据**:\n")
		// 	sb.WriteString(formatMarketDataForMultiTimeframe(data.DailyData))
		// 	sb.WriteString("\n")
		// }
		
		// 4小时数据（完整序列）
		if data.Hourly4Data != nil {
			sb.WriteString("**4小时 (4h) 数据**:\n")
			sb.WriteString(formatMarketDataForMultiTimeframe(data.Hourly4Data))
			sb.WriteString("\n")
		}
		
		// 1小时数据（完整序列）
		if data.Hourly1Data != nil {
			sb.WriteString("**1小时 (1h) 数据**:\n")
			sb.WriteString(formatMarketDataForMultiTimeframe(data.Hourly1Data))
			sb.WriteString("\n")
		}
		
		// 15分钟数据（完整序列）
		if data.Minute15Data != nil {
			sb.WriteString("**15分钟 (15m) 数据**:\n")
			sb.WriteString(formatMarketDataForMultiTimeframe(data.Minute15Data))
			sb.WriteString("\n")
		}
		
		// 3分钟数据（完整序列）- 已注释，不再发送给AI
		// if data.Minute3Data != nil {
		// 	sb.WriteString("**3分钟 (3m) 数据**:\n")
		// 	sb.WriteString(formatMarketDataForMultiTimeframe(data.Minute3Data))
		// 	sb.WriteString("\n")
		// }
	}
	
	// ==================== AI学习和进化数据 ====================
	// 每次决策前分析最近20个交易周期，让AI能够学习和进化
	if ctx.Performance != nil {
		// 方法1: 直接类型断言（如果Performance是*logger.PerformanceAnalysis）
		if perf, ok := ctx.Performance.(*logger.PerformanceAnalysis); ok {
			sb.WriteString("## 📚 历史表现分析（AI学习数据）\n\n")
			
			// 1. 总体统计
			sb.WriteString("### 📊 总体表现\n\n")
			if perf.TotalTrades > 0 {
				sb.WriteString(fmt.Sprintf("- **总交易数**: %d\n", perf.TotalTrades))
				sb.WriteString(fmt.Sprintf("- **盈利交易**: %d\n", perf.WinningTrades))
				sb.WriteString(fmt.Sprintf("- **亏损交易**: %d\n", perf.LosingTrades))
				sb.WriteString(fmt.Sprintf("- **胜率**: %.1f%%\n", perf.WinRate))
				sb.WriteString(fmt.Sprintf("- **平均盈利**: %.2f USDT\n", perf.AvgWin))
				sb.WriteString(fmt.Sprintf("- **平均亏损**: %.2f USDT\n", perf.AvgLoss))
				sb.WriteString(fmt.Sprintf("- **盈亏比**: %.2f\n", perf.ProfitFactor))
				sb.WriteString(fmt.Sprintf("- **夏普比率**: %.2f\n\n", perf.SharpeRatio))
			} else {
				sb.WriteString("- **总交易数**: 0（暂无已完成的历史交易记录）\n\n")
			}
			
			// 2. 各币种详细统计（只显示候选币种的统计，用于根据胜率优化仓位大小）
			if len(perf.SymbolStats) > 0 && len(ctx.CandidateCoins) > 0 {
				// 构建候选币种集合
				candidateSymbols := make(map[string]bool)
				for _, coin := range ctx.CandidateCoins {
					candidateSymbols[coin.Symbol] = true
				}
				
				// 按总盈亏排序
				type SymbolStat struct {
					Symbol string
					Stats  *logger.SymbolPerformance
				}
				var sortedStats []SymbolStat
				for symbol, stats := range perf.SymbolStats {
					// 只包含候选币种的统计
					if candidateSymbols[symbol] && stats.TotalTrades > 0 {
						sortedStats = append(sortedStats, SymbolStat{Symbol: symbol, Stats: stats})
					}
				}
				
				if len(sortedStats) > 0 {
					sb.WriteString("### 📈 各币种表现统计（仅候选币种，用于仓位优化）\n\n")
					sb.WriteString("**根据胜率优化仓位大小**：表现好的币种可以适当增加仓位，表现差的币种应该减少或避免交易。\n\n")
					
					// 简单排序（按总盈亏降序）
					for i := 0; i < len(sortedStats)-1; i++ {
						for j := i + 1; j < len(sortedStats); j++ {
							if sortedStats[i].Stats.TotalPnL < sortedStats[j].Stats.TotalPnL {
								sortedStats[i], sortedStats[j] = sortedStats[j], sortedStats[i]
							}
						}
					}
					
					// 显示所有候选币种（不再限制为10个）
					for i := 0; i < len(sortedStats); i++ {
						stat := sortedStats[i]
						sb.WriteString(fmt.Sprintf("- **%s**: 交易%d次, 胜率%.1f%%, 总盈亏%.2f USDT, 平均%.2f USDT/笔\n",
							stat.Symbol, stat.Stats.TotalTrades, stat.Stats.WinRate, stat.Stats.TotalPnL, stat.Stats.AvgPnL))
					}
					sb.WriteString("\n")
				}
			}
			
			// 3. 最近交易记录（显示最近5条，不限币种）
			if len(perf.RecentTrades) > 0 {
				// 按CloseTime降序排序（最新的在前）
				sortedTrades := make([]logger.TradeOutcome, len(perf.RecentTrades))
				copy(sortedTrades, perf.RecentTrades)
				
				// 简单排序（按CloseTime降序）
				for i := 0; i < len(sortedTrades)-1; i++ {
					for j := i + 1; j < len(sortedTrades); j++ {
						if sortedTrades[i].CloseTime.Before(sortedTrades[j].CloseTime) {
							sortedTrades[i], sortedTrades[j] = sortedTrades[j], sortedTrades[i]
						}
					}
				}
				
				// 只取前5条
				displayCount := len(sortedTrades)
				if displayCount > 5 {
					displayCount = 5
				}
				
				if displayCount > 0 {
					sb.WriteString("### 📝 最近交易记录（最近5条）\n\n")
					for i := 0; i < displayCount; i++ {
						trade := sortedTrades[i]
						pnlSign := "+"
						if trade.PnL < 0 {
							pnlSign = ""
						}
						stopLossMark := ""
						if trade.WasStopLoss {
							stopLossMark = " 🛑"
						}
						closeTimeStr := trade.CloseTime.Format("2006-01-02 15:04:05")
						
						// 平仓逻辑（使用CloseReason，已在performance_analysis.go中按优先级填充）
						closeLogic := ""
						if trade.CloseReason != "" {
							closeLogic = fmt.Sprintf(" | 平仓逻辑: %s", trade.CloseReason)
						} else {
							// 如果CloseReason为空，显示默认值（虽然理论上不应该为空）
							closeLogic = " | 平仓逻辑: 未提供平仓逻辑"
						}
						
						sb.WriteString(fmt.Sprintf("%d. **%s** %s | 开仓: %.2f → 平仓: %.2f | 盈亏: %s%.2f USDT (%.2f%%) | 杠杆: %dx | 时长: %s | 平仓时间: %s%s%s\n",
							i+1, trade.Symbol, trade.Side, trade.OpenPrice, trade.ClosePrice,
							pnlSign, trade.PnL, trade.PnLPct, trade.Leverage, trade.Duration, closeTimeStr, stopLossMark, closeLogic))
					}
					sb.WriteString("\n")
				}
			}
			
			// 策略建议应该从策略文件中读取，而不是硬编码
			// 这里只显示当前夏普比率，让AI根据策略文件中的指导自行判断
			sb.WriteString("### 🎯 当前表现指标\n\n")
			sb.WriteString(fmt.Sprintf("**当前夏普比率**: %.2f\n\n", perf.SharpeRatio))
			
			log.Printf("📚 已添加AI学习数据: 总交易数=%d, 胜率=%.1f%%, 夏普比率=%.2f, 最近交易记录=%d条", 
				perf.TotalTrades, perf.WinRate, perf.SharpeRatio, len(perf.RecentTrades))
		} else {
			// 方法2: 通过JSON解析（兼容性方案）
			type PerformanceData struct {
				TotalTrades   int                           `json:"total_trades"`
				WinningTrades int                           `json:"winning_trades"`
				LosingTrades  int                           `json:"losing_trades"`
				WinRate       float64                       `json:"win_rate"`
				SharpeRatio   float64                       `json:"sharpe_ratio"`
				RecentTrades  []logger.TradeOutcome         `json:"recent_trades"`
				SymbolStats   map[string]*logger.SymbolPerformance `json:"symbol_stats"`
				BestSymbol    string                        `json:"best_symbol"`
				WorstSymbol    string                        `json:"worst_symbol"`
			}
			var perfData PerformanceData
			if jsonData, err := json.Marshal(ctx.Performance); err == nil {
				if err := json.Unmarshal(jsonData, &perfData); err == nil {
					sb.WriteString("## 📚 历史表现分析（AI学习数据）\n\n")
					
					// 1. 总体统计
					sb.WriteString("### 📊 总体表现\n\n")
					if perfData.TotalTrades > 0 {
						sb.WriteString(fmt.Sprintf("- **总交易数**: %d\n", perfData.TotalTrades))
						sb.WriteString(fmt.Sprintf("- **胜率**: %.1f%%\n", perfData.WinRate))
						sb.WriteString(fmt.Sprintf("- **夏普比率**: %.2f\n\n", perfData.SharpeRatio))
						if perfData.BestSymbol != "" {
							sb.WriteString(fmt.Sprintf("**表现最好**: %s\n", perfData.BestSymbol))
						}
						if perfData.WorstSymbol != "" {
							sb.WriteString(fmt.Sprintf("**表现最差**: %s\n", perfData.WorstSymbol))
						}
					} else {
						sb.WriteString("- **总交易数**: 0（暂无已完成的历史交易记录）\n\n")
					}
					
					// 最近交易记录（显示最近5条，不限币种）
					if len(perfData.RecentTrades) > 0 {
						// 按CloseTime降序排序（最新的在前）
						sortedTrades := make([]logger.TradeOutcome, len(perfData.RecentTrades))
						copy(sortedTrades, perfData.RecentTrades)
						
						// 简单排序（按CloseTime降序）
						for i := 0; i < len(sortedTrades)-1; i++ {
							for j := i + 1; j < len(sortedTrades); j++ {
								if sortedTrades[i].CloseTime.Before(sortedTrades[j].CloseTime) {
									sortedTrades[i], sortedTrades[j] = sortedTrades[j], sortedTrades[i]
								}
							}
						}
						
						// 只取前5条
						displayCount := len(sortedTrades)
						if displayCount > 5 {
							displayCount = 5
						}
						
						if displayCount > 0 {
							sb.WriteString("\n### 📝 最近交易记录（最近5条）\n\n")
							for i := 0; i < displayCount; i++ {
								trade := sortedTrades[i]
								pnlSign := "+"
								if trade.PnL < 0 {
									pnlSign = ""
								}
								stopLossMark := ""
								if trade.WasStopLoss {
									stopLossMark = " 🛑"
								}
								closeTimeStr := trade.CloseTime.Format("2006-01-02 15:04:05")
								
								// 平仓逻辑（使用CloseReason，已在performance_analysis.go中按优先级填充）
								closeLogic := ""
								if trade.CloseReason != "" {
									closeLogic = fmt.Sprintf(" | 平仓逻辑: %s", trade.CloseReason)
								} else {
									// 如果CloseReason为空，显示默认值（虽然理论上不应该为空）
									closeLogic = " | 平仓逻辑: 未提供平仓逻辑"
								}
								
								sb.WriteString(fmt.Sprintf("%d. **%s** %s | 开仓: %.2f → 平仓: %.2f | 盈亏: %s%.2f USDT (%.2f%%) | 杠杆: %dx | 时长: %s | 平仓时间: %s%s%s\n",
									i+1, trade.Symbol, trade.Side, trade.OpenPrice, trade.ClosePrice,
									pnlSign, trade.PnL, trade.PnLPct, trade.Leverage, trade.Duration, closeTimeStr, stopLossMark, closeLogic))
							}
							sb.WriteString("\n")
						}
					}
					
					// 策略建议应该从策略文件中读取，而不是硬编码
					// 这里只显示当前夏普比率，让AI根据策略文件中的指导自行判断
					if perfData.TotalTrades > 0 {
						sb.WriteString("### 🎯 当前表现指标\n\n")
						sb.WriteString(fmt.Sprintf("**当前夏普比率**: %.2f\n\n", perfData.SharpeRatio))
					}
					
					log.Printf("📊 通过JSON解析获取Performance数据，最近交易记录=%d条", len(perfData.RecentTrades))
				} else {
					log.Printf("⚠️  JSON解析Performance失败: %v", err)
				}
			} else {
				log.Printf("⚠️  JSON序列化Performance失败: %v", err)
			}
		}
	} else {
		log.Printf("ℹ️  Performance数据为空，无法显示历史表现分析")
	}
	
	// 最近的强制平仓记录
	if len(ctx.RecentForcedCloses) > 0 {
		sb.WriteString("## 🛑 最近的强制平仓记录\n\n")
		for i, forcedClose := range ctx.RecentForcedCloses {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, forcedClose))
		}
		sb.WriteString("\n")
	}
	
	sb.WriteString("---\n\n")
	sb.WriteString("请基于多时间框架分析结果输出决策（思维链 + JSON）\n")
	// 注释掉一致性评分的提示，让AI自己判断
	// 已注释：去掉评分系统推荐方向的提示，让AI完全基于数据自行判断
	// sb.WriteString("**注意**: 评分系统已为您分析出推荐方向（做多/做空），请结合详细数据进行决策。\n")
	// sb.WriteString("**注意**: 评分系统已为您分析出推荐方向（做多/做空），请结合一致性评分和详细数据进行决策。\n")
	
	return sb.String(), nil
}

// parseFullDecisionResponse 解析AI的完整决策响应
func parseFullDecisionResponse(aiResponse string, accountEquity float64, btcEthLeverage, altcoinLeverage int) (*FullDecision, error) {
	// 1. 提取思维链
	cotTrace := extractCoTTrace(aiResponse)

	// 2. 提取JSON决策列表
	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("提取决策失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	// 3. 验证决策（需要市场数据用于入场价验证）
	if err := validateDecisionsWithMarketData(decisions, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("决策验证失败: %w\n\n=== AI思维链分析 ===\n%s", err, cotTrace)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

// formatMarketDataForMultiTimeframe 格式化市场数据用于多时间框架显示
// 直接使用market.Format函数，确保包含所有数据（DIF、DEA、HIST、成交量序列等）
// 但移除 "Longer‑term context" 部分，避免在每个时间框架中重复显示相同内容
func formatMarketDataForMultiTimeframe(data *market.Data) string {
	// 使用market.Format函数，它会自动包含所有序列数据
	formatted := market.Format(data)
	
	// 移除 "Longer‑term context" 部分（从该行开始到字符串结尾）
	// 避免在每个时间框架（1D, 4H, 1H, 15M）中都重复显示相同的内容
	longerTermIndex := strings.Index(formatted, "Longer‑term context")
	if longerTermIndex >= 0 {
		// 找到该部分，只保留之前的内容
		formatted = formatted[:longerTermIndex]
		// 移除末尾可能的空行
		formatted = strings.TrimRight(formatted, " \n\r\t")
	}
	
	// 添加缩进，使其在多时间框架显示中更清晰
	lines := strings.Split(formatted, "\n")
	var result strings.Builder
	for _, line := range lines {
		if line != "" {
			result.WriteString("   ")
			result.WriteString(line)
			result.WriteString("\n")
		}
	}
	return result.String()
}

// calculateSingleTimeframeScore 计算单个时间框架的质量评分
// 重构版本：修复RSI评分逻辑，改进评分算法
func calculateSingleTimeframeScore(data *market.Data) float64 {
	if data == nil {
		return 0.5 // 默认中等评分
	}

	score := 0.0
	count := 0

	// 1. 价格与EMA关系（趋势强度）- 权重最高
	if data.CurrentEMA20 > 0 && data.CurrentPrice > 0 {
		emaRatio := (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20
		if emaRatio > 0.02 { // 价格远高于EMA，看涨趋势强
			score += 0.8
		} else if emaRatio > 0 { // 价格高于EMA，看涨趋势
			score += 0.6
		} else if emaRatio < -0.02 { // 价格远低于EMA，看跌趋势强
			score += 0.2 // 对于做空来说是好机会，但评分仍较低（因为这是做多评分）
		} else { // 价格低于EMA，看跌趋势
			score += 0.4
		}
		count++
	}

	// 2. MACD趋势
	if data.CurrentMACD != 0 {
		if data.CurrentMACD > 0 {
			score += 0.7 // 正MACD通常表示上升趋势
		} else {
			score += 0.3 // 负MACD通常表示下降趋势
		}
		count++
	}

	// 3. RSI位置 (修复逻辑：移除永远不会执行的else分支)
	if data.CurrentRSI7 > 0 {
		if data.CurrentRSI7 > 30 && data.CurrentRSI7 < 70 {
			// RSI在健康区间（30-70），加分
			score += 0.8
		} else if data.CurrentRSI7 >= 70 {
			// RSI超买（>=70），对做多不利，减分
			score += 0.2
		} else if data.CurrentRSI7 <= 30 {
			// RSI超卖（<=30），对做多有利（反弹机会），但评分仍较低（因为可能过于极端）
			// 超卖区域可能意味着深度回调，需要谨慎
			score += 0.3
		}
		count++
	}

	// 4. 如果没有任何有效指标，返回默认值
	if count == 0 {
		return 0.5
	}

	// 计算平均分
	score = score / float64(count)

	// 限制在0-1范围内
	if score < 0 {
		score = 0
	} else if score > 1 {
		score = 1
	}

	return score
}


// extractCoTTrace 提取思维链分析
func extractCoTTrace(response string) string {
	// 查找JSON数组的开始位置
	jsonStart := strings.Index(response, "[")

	if jsonStart > 0 {
		// 思维链是JSON数组之前的内容
		return strings.TrimSpace(response[:jsonStart])
	}

	// 如果找不到JSON，整个响应都是思维链
	return strings.TrimSpace(response)
}

// extractDecisions 提取JSON决策列表
func extractDecisions(response string) ([]Decision, error) {
	// 直接查找JSON数组 - 找第一个完整的JSON数组
	arrayStart := strings.Index(response, "[")
	if arrayStart == -1 {
		return nil, fmt.Errorf("无法找到JSON数组起始")
	}

	// 从 [ 开始，匹配括号找到对应的 ]
	arrayEnd := findMatchingBracket(response, arrayStart)
	if arrayEnd == -1 {
		return nil, fmt.Errorf("无法找到JSON数组结束")
	}

	jsonContent := strings.TrimSpace(response[arrayStart : arrayEnd+1])

	// 🔧 修复常见的JSON格式错误：缺少引号的字段值
	// 匹配: "reasoning": 内容"}  或  "reasoning": 内容}  (没有引号)
	// 修复为: "reasoning": "内容"}
	// 使用简单的字符串扫描而不是正则表达式
	jsonContent = fixMissingQuotes(jsonContent)

	// 解析JSON
	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w\nJSON内容: %s", err, jsonContent)
	}

	return decisions, nil
}

// fixMissingQuotes 替换中文引号为英文引号（避免输入法自动转换）
func fixMissingQuotes(jsonStr string) string {
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"") // "
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")  // '
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")  // '
	return jsonStr
}

// validateDecisionsWithMarketData 验证所有决策（使用市场数据获取实际价格）
func validateDecisionsWithMarketData(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	for i, decision := range decisions {
		if err := validateDecisionWithMarketData(&decision, accountEquity, btcEthLeverage, altcoinLeverage); err != nil {
			return fmt.Errorf("决策 #%d 验证失败: %w", i+1, err)
		}
	}
	return nil
}

// validateDecisions 验证所有决策（兼容旧接口，内部调用新接口）
func validateDecisions(decisions []Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	return validateDecisionsWithMarketData(decisions, accountEquity, btcEthLeverage, altcoinLeverage)
}

// findMatchingBracket 查找匹配的右括号
func findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// validateDecisionWithMarketData 验证单个决策的有效性（使用实际市场价格）
func validateDecisionWithMarketData(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	// 验证action
	validActions := map[string]bool{
		"open_long":   true,
		"open_short":  true,
		"close_long":  true,
		"close_short": true,
		"update_tp":   true, // 更新止盈
		"update_sl":   true, // 更新止损
		"hold":        true,
		"wait":        true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("无效的action: %s", d.Action)
	}

	// 开仓操作必须提供完整参数
	if d.Action == "open_long" || d.Action == "open_short" {
		// 根据币种使用配置的杠杆上限
		maxLeverage := altcoinLeverage          // 山寨币使用配置的杠杆
		maxPositionValue := accountEquity * float64(altcoinLeverage) * 0.9 // 山寨币最多配置杠杆的90% * 账户净值
		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			maxLeverage = btcEthLeverage          // BTC和ETH使用配置的杠杆
			maxPositionValue = accountEquity * float64(btcEthLeverage) * 0.9 // BTC/ETH最多配置杠杆的90% * 账户净值
		}

		if d.Leverage <= 0 || d.Leverage > maxLeverage {
			return fmt.Errorf("杠杆必须在1-%d之间（%s，当前配置上限%d倍）: %d", maxLeverage, d.Symbol, maxLeverage, d.Leverage)
		}
		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("仓位大小必须大于0: %.2f", d.PositionSizeUSD)
		}
		
		// 验证保证金使用率（主要验证逻辑）
		// 保证金 = 仓位价值 / 杠杆
		marginRequired := d.PositionSizeUSD / float64(d.Leverage)
		// 使用50%保证金使用率限制（适用于单币种模式的更安全限制）
		maxMarginUsedPct := 50.0 
		maxMarginAllowed := accountEquity * (maxMarginUsedPct / 100.0)
		
		// 验证保证金使用率（加1%容差以避免浮点数精度问题）
		tolerance_margin := maxMarginAllowed * 0.01 // 1%容差
		if marginRequired > maxMarginAllowed+tolerance_margin {
			return fmt.Errorf("%s仓位保证金不能超过%.0f USDT（%.0f%%保证金使用率，单币种模式限制），实际: %.0f USDT（仓位%.0f USDT，%dx杠杆）", 
				d.Symbol, maxMarginAllowed, maxMarginUsedPct, marginRequired, d.PositionSizeUSD, d.Leverage)
		}
		
		// 验证仓位价值上限（加1%容差以避免浮点数精度问题）- 作为第二道安全防线
		tolerance := maxPositionValue * 0.01 // 1%容差
		if d.PositionSizeUSD > maxPositionValue+tolerance {
			// 计算实际杠杆倍数
			effectiveLeverage := d.PositionSizeUSD / accountEquity
			if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
				return fmt.Errorf("BTC/ETH单币种仓位价值不能超过%.0f USDT（%.1f倍账户净值），实际: %.0f USDT（%.1f倍账户净值）", 
					maxPositionValue, maxPositionValue/accountEquity, d.PositionSizeUSD, effectiveLeverage)
			} else {
				return fmt.Errorf("山寨币单币种仓位价值不能超过%.0f USDT（%.1f倍账户净值），实际: %.0f USDT（%.1f倍账户净值）", 
					maxPositionValue, maxPositionValue/accountEquity, d.PositionSizeUSD, effectiveLeverage)
			}
		}
		
		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("止损和止盈必须大于0")
		}

		// 验证止损止盈的合理性
		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("做多时止损价必须小于止盈价")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("做空时止损价必须大于止盈价")
			}
		}

		// 验证入场价在止损和止盈之间（合理范围）
		// 注意：不再硬编码风险回报比检查，相信AI会根据提示词自行判断
		currentPrice, err := getCurrentMarketPrice(d.Symbol)
		if err != nil {
			// 如果获取价格失败，拒绝该决策（避免使用不准确的价格进行验证）
			return fmt.Errorf("获取 %s 当前价格失败: %v，拒绝该决策以确保安全性", d.Symbol, err)
		}
		
		// 验证入场价在止损和止盈之间（合理范围）
		entryPriceValid := false
		if d.Action == "open_long" {
			// 做多：入场价应该在止损和止盈之间
			if currentPrice > d.StopLoss && currentPrice < d.TakeProfit {
				entryPriceValid = true
			}
		} else {
			// 做空：入场价应该在止损和止盈之间
			if currentPrice > d.TakeProfit && currentPrice < d.StopLoss {
				entryPriceValid = true
			}
		}
		
		if !entryPriceValid {
			return fmt.Errorf("当前市场价格%.4f不在止损%.4f和止盈%.4f的合理范围内（%s）",
				currentPrice, d.StopLoss, d.TakeProfit, d.Action)
		}
	}

	// 验证update_tp操作
	if d.Action == "update_tp" {
		if d.TakeProfit <= 0 {
			return fmt.Errorf("update_tp必须提供有效的take_profit价格: %.4f", d.TakeProfit)
		}
		// 验证持仓是否存在（这会在执行时检查，这里只验证参数）
		if d.Symbol == "" {
			return fmt.Errorf("update_tp必须提供symbol")
		}
	}

	// 验证update_sl操作
	if d.Action == "update_sl" {
		if d.StopLoss <= 0 {
			return fmt.Errorf("update_sl必须提供有效的stop_loss价格: %.4f", d.StopLoss)
		}
		// 验证持仓是否存在（这会在执行时检查，这里只验证参数）
		if d.Symbol == "" {
			return fmt.Errorf("update_sl必须提供symbol")
		}
	}

	return nil
}

// validateDecision 验证单个决策的有效性（兼容旧接口）
func validateDecision(d *Decision, accountEquity float64, btcEthLeverage, altcoinLeverage int) error {
	return validateDecisionWithMarketData(d, accountEquity, btcEthLeverage, altcoinLeverage)
}

// getCurrentMarketPrice 获取当前市场价格
func getCurrentMarketPrice(symbol string) (float64, error) {
	marketData, err := market.Get(symbol)
	if err != nil {
		return 0, fmt.Errorf("获取市场数据失败: %w", err)
	}
	if marketData.CurrentPrice <= 0 {
		return 0, fmt.Errorf("当前价格无效: %.4f", marketData.CurrentPrice)
	}
	return marketData.CurrentPrice, nil
}
