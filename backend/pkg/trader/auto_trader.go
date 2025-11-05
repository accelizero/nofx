package trader

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"backend/pkg/config"
	"backend/pkg/decision"
	"backend/pkg/logger"
	"backend/pkg/market"
	"backend/pkg/mcp"
	"backend/pkg/pool"
	"backend/pkg/storage"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AutoTraderConfig 自动交易配置（简化版 - AI全权决策）
type AutoTraderConfig struct {
	// Trader标识
	ID      string // Trader唯一标识（用于日志目录等）
	Name    string // Trader显示名称
	AIModel string // AI模型: "qwen" 或 "deepseek"

	// 交易平台选择
	Exchange string // "aster"

	// Aster配置
	AsterUser       string // Aster主钱包地址
	AsterSigner     string // Aster API钱包地址
	AsterPrivateKey string // Aster API钱包私钥

	// AI配置
	UseQwen     bool
	DeepSeekKey string
	QwenKey     string

	// 自定义AI API配置
	CustomAPIURL    string
	CustomAPIKey    string
	CustomModelName string

	// 扫描配置
	ScanInterval time.Duration // 扫描间隔（建议3分钟）

	// 账户配置
	InitialBalance float64 // 初始金额（用于计算盈亏，需手动设置）

	// 杠杆配置
	BTCETHLeverage  int // BTC和ETH的杠杆倍数
	AltcoinLeverage int // 山寨币的杠杆倍数

	// 风险控制（强制止损止盈）
	MaxDailyLoss         float64       // 最大日亏损百分比（账户级别风控）
	MaxDrawdown          float64       // 最大回撤百分比（账户级别风控）
	PositionStopLossPct  float64       // 单仓位止损百分比（单仓位亏损超过此值时强制平仓，默认10%）
	PositionTakeProfitPct float64      // 单仓位止盈百分比（可选，>0时强制止盈，≤0时由AI自行判断）
	StopTradingTime      time.Duration // 触发风控后暂停时长
	
	// 流动性过滤配置
	SkipLiquidityCheck  bool           // 是否跳过流动性检查（默认false，开启后可以交易流动性差的币种）
	
	// 分析模式配置
	AnalysisMode        string         // 分析模式："standard" 或 "multi_timeframe"
	MultiTimeframeConfig *config.MultiTimeframeConfig // 多时间框架配置（仅在mode="multi_timeframe"时有效）
	
	// 策略配置
	StrategyName       string // 策略名称（从配置读取）
	StrategyPreference string // 策略偏好（从配置读取）
}

// AutoTrader 自动交易器
type AutoTrader struct {
	id                    string // Trader唯一标识
	name                  string // Trader显示名称
	aiModel               string // AI模型名称
	exchange              string // 交易平台名称
	config                AutoTraderConfig
	trader                Trader // 使用Trader接口（支持多平台）
	mcpClient             *mcp.Client
	positionLogicManager  *storage.PositionLogicWrapper // 持仓逻辑管理器（使用数据库存储）
	storageAdapter        *storage.StorageAdapter // 数据库存储适配器
	initialBalance        float64
	dailyPnL              float64          // 日盈亏（需要并发保护）
	dailyStartEquity      float64          // 每日开始时的净值（用于计算日盈亏）
	lastResetTime         time.Time
	stopUntil             time.Time
	isRunning             int32            // 运行状态（使用atomic保护，1=运行中，0=已停止）
	startTime             time.Time        // 系统启动时间
	callCount             int64            // AI调用次数（使用atomic保护）
	positionFirstSeenTime map[string]int64 // 持仓首次出现时间 (symbol_side -> timestamp毫秒)
	positionTimeMu        sync.RWMutex     // 保护positionFirstSeenTime的并发访问
	peakEquity            float64          // 峰值净值（用于计算回撤）
	riskMu                sync.RWMutex     // 保护peakEquity和dailyPnL的并发访问
	forcedClosedPositions map[string]time.Time // 已强制平仓的持仓（symbol_side -> 标记时间），失败时记录失败时间，5分钟后可重试
	forcedCloseMu         sync.RWMutex          // 保护forcedClosedPositions的并发访问
	closingPositions      map[string]*sync.Mutex // 正在执行平仓的持仓锁（symbol_side -> Mutex），防止并发平仓
	closingPositionsMu    sync.Mutex       // 保护closingPositions的并发访问
	savePositionTimeMu    sync.Mutex       // 保护savePositionFirstSeenTime的并发调用
}

// NewAutoTrader 创建自动交易器
func NewAutoTrader(config AutoTraderConfig) (*AutoTrader, error) {
	// 设置默认值
	if config.ID == "" {
		config.ID = "default_trader"
	}
	if config.Name == "" {
		config.Name = "Default Trader"
	}
	if config.AIModel == "" {
		if config.UseQwen {
			config.AIModel = "qwen"
		} else {
			config.AIModel = "deepseek"
		}
	}

	mcpClient := mcp.New()

	// 初始化AI并验证密钥（在初始化时验证，避免运行时才发现配置错误）
	if config.AIModel == "custom" {
		// 使用自定义API
		if config.CustomAPIURL == "" {
			return nil, fmt.Errorf("使用自定义AI时必须配置custom_api_url")
		}
		if config.CustomAPIKey == "" {
			return nil, fmt.Errorf("使用自定义AI时必须配置custom_api_key")
		}
		if config.CustomModelName == "" {
			return nil, fmt.Errorf("使用自定义AI时必须配置custom_model_name")
		}
		mcpClient.SetCustomAPI(config.CustomAPIURL, config.CustomAPIKey, config.CustomModelName)
		log.Printf("🤖 [%s] 使用自定义AI API: %s (模型: %s)", config.Name, config.CustomAPIURL, config.CustomModelName)
	} else if config.UseQwen || config.AIModel == "qwen" {
		// 使用Qwen
		if config.QwenKey == "" {
			return nil, fmt.Errorf("使用Qwen时必须配置qwen_key")
		}
		mcpClient.SetQwenAPIKey(config.QwenKey, "")
		log.Printf("🤖 [%s] 使用阿里云Qwen AI", config.Name)
	} else {
		// 默认使用DeepSeek
		if config.DeepSeekKey == "" {
			return nil, fmt.Errorf("使用DeepSeek时必须配置deepseek_key")
		}
		mcpClient.SetDeepSeekAPIKey(config.DeepSeekKey)
		log.Printf("🤖 [%s] 使用DeepSeek AI", config.Name)
	}

	// 设置默认交易平台
	if config.Exchange == "" {
		config.Exchange = "aster"
	}

	// 根据配置创建对应的交易器
	var trader Trader
	var err error

	if config.Exchange != "aster" {
		return nil, fmt.Errorf("不支持的交易平台: %s，当前仅支持aster", config.Exchange)
	}

	log.Printf("🏦 [%s] 使用Aster交易", config.Name)
	trader, err = NewAsterTrader(config.AsterUser, config.AsterSigner, config.AsterPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("初始化Aster交易器失败: %w", err)
	}
	// 设置市场数据API使用Aster
	market.SetExchange("aster")

	// 验证初始金额配置
	if config.InitialBalance <= 0 {
		return nil, fmt.Errorf("初始金额必须大于0，请在配置中设置InitialBalance")
	}

	// 初始化数据库存储适配器
	storageAdapter, err := storage.NewStorageAdapter("data")
	if err != nil {
		return nil, fmt.Errorf("初始化存储适配器失败: %w", err)
	}

	// 初始化持仓逻辑管理器（使用数据库存储）
	positionLogicStorage := storageAdapter.GetPositionLogicStorage()
	if positionLogicStorage == nil {
		return nil, fmt.Errorf("获取持仓逻辑存储失败")
	}
	logicManager := storage.NewPositionLogicWrapper(positionLogicStorage)

	// 从数据库加载持仓首次出现时间（迁移旧数据）
	positionFirstSeenTime := make(map[string]int64)
	allTimes, err := positionLogicStorage.GetAllFirstSeenTimes()
	if err == nil && len(allTimes) > 0 {
		positionFirstSeenTime = allTimes
		log.Printf("📅 已从数据库加载 %d 个持仓的开仓时间", len(allTimes))
	}

	return &AutoTrader{
		id:                    config.ID,
		name:                  config.Name,
		aiModel:               config.AIModel,
		exchange:              config.Exchange,
		config:                config,
		trader:                trader,
		mcpClient:             mcpClient,
		positionLogicManager:   logicManager,
		storageAdapter:        storageAdapter,
		initialBalance:        config.InitialBalance,
		dailyStartEquity:       config.InitialBalance, // 每日开始时的净值
		lastResetTime:         time.Now(),
		startTime:             time.Now(),
		callCount:             0,
		isRunning:             0, // 0 = 未运行
		positionFirstSeenTime: positionFirstSeenTime,
		peakEquity:            config.InitialBalance, // 初始峰值 = 初始余额
		forcedClosedPositions: make(map[string]time.Time),
		closingPositions:      make(map[string]*sync.Mutex),
		stopUntil:             time.Time{}, // 初始化为零值，表示未设置暂停状态（重启后重置）
	}, nil
}

// savePositionFirstSeenTime 保存持仓首次出现时间到数据库（已废弃，现在直接保存）
// 保留此方法用于兼容，但实际不再需要批量保存
func (at *AutoTrader) savePositionFirstSeenTime() {
	// 现在每次设置时间时都直接保存到数据库，不再需要批量保存
}

// Run 运行自动交易主循环
func (at *AutoTrader) Run() error {
	atomic.StoreInt32(&at.isRunning, 1)
	log.Println("🚀 AI驱动自动交易系统启动")
	log.Printf("💰 初始余额: %.2f USDT", at.initialBalance)
	log.Printf("⚙️  扫描间隔: %v", at.config.ScanInterval)
	log.Println("🤖 AI将全权决定杠杆、仓位大小、止损止盈等参数")
	log.Println("🛡️  单仓位止损检查：每10秒执行一次（独立于AI决策周期，快速响应插针行情）")

	// 主循环定时器（AI决策周期）
	ticker := time.NewTicker(at.config.ScanInterval)
	defer ticker.Stop()

	// 单仓位止损检查定时器（每10秒执行，快速响应插针行情）
	stopLossTicker := time.NewTicker(10 * time.Second)
	defer stopLossTicker.Stop()

	// 首次立即执行AI决策周期
	if err := at.runCycle(); err != nil {
		log.Printf("❌ 执行失败: %v", err)
	}

	// 首次立即执行单仓位止损检查
	at.checkPositionStopLossOnly()

	for atomic.LoadInt32(&at.isRunning) == 1 {
		select {
		case <-ticker.C:
			// AI决策周期
			if err := at.runCycle(); err != nil {
				log.Printf("❌ 执行失败: %v", err)
			}
		case <-stopLossTicker.C:
			// 单仓位止损检查（每10秒执行，快速响应插针行情）
			at.checkPositionStopLossOnly()
		}
	}

	return nil
}

// Stop 停止自动交易
func (at *AutoTrader) Stop() {
	atomic.StoreInt32(&at.isRunning, 0)
	log.Println("⏹ 自动交易系统停止")
}

// runCycle 运行一个交易周期（使用AI全权决策）
func (at *AutoTrader) runCycle() error {
	atomic.AddInt64(&at.callCount, 1)

	cycleNum := atomic.LoadInt64(&at.callCount)
	now := time.Now()
	log.Printf("\n" + strings.Repeat("=", 70))
	log.Printf("⏰ %s - AI决策周期 #%d", now.Format("2006-01-02 15:04:05"), cycleNum)
	log.Printf(strings.Repeat("=", 70))

	// 创建决策记录
	record := &logger.DecisionRecord{
		Timestamp:      now,
		CycleNumber:    int(cycleNum),
		ExecutionLog:   []string{},
		Positions:      []logger.PositionSnapshot{}, // 初始化为空slice
		Decisions:      []logger.DecisionAction{},
		CandidateCoins: []string{},
		Success:        true,
	}

	// 1. 检查是否需要停止交易
	// 注意：stopUntil 只在本次运行期间有效，重启后应该重置
	// 使用 IsZero() 检查是否为未设置状态（重启后的情况）
	if !at.stopUntil.IsZero() && time.Now().Before(at.stopUntil) {
		remaining := at.stopUntil.Sub(time.Now())
		log.Printf("⏸ 风险控制：暂停交易中，剩余 %.0f 分钟", remaining.Minutes())
		
		// 尝试获取账户状态（即使暂停交易也要显示账户信息）
		ctx, err := at.buildTradingContext()
		if err == nil && ctx != nil {
			record.AccountState = logger.AccountSnapshot{
				TotalBalance:          ctx.Account.TotalEquity,
				AvailableBalance:      ctx.Account.AvailableBalance,
				TotalUnrealizedProfit: ctx.Account.TotalPnL,
				PositionCount:         ctx.Account.PositionCount,
				MarginUsedPct:         ctx.Account.MarginUsedPct,
			}
		}
		
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("风险控制暂停中，剩余 %.0f 分钟", remaining.Minutes())
		return nil
	}

	// 2. 检查日盈亏重置（在构建上下文之前，避免构建失败时无法重置）
	needResetDailyPnL := time.Since(at.lastResetTime) > 24*time.Hour
	
	// 2.5. 收集交易上下文（先获取持仓数据用于强制止损检查）
	ctx, err := at.buildTradingContext()
	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("构建交易上下文失败: %v", err)
		
		// 即使构建上下文失败，也尝试重置日盈亏（使用上次记录的净值或初始余额作为fallback）
		if needResetDailyPnL {
			// 使用初始余额作为fallback，至少保证日盈亏计算不会出错
			at.riskMu.Lock()
			at.dailyStartEquity = at.initialBalance
			at.dailyPnL = 0
			at.peakEquity = at.initialBalance
			at.riskMu.Unlock()
			at.lastResetTime = time.Now()
			log.Printf("📅 日盈亏已重置（构建上下文失败，使用初始余额作为fallback）: %.2f USDT", at.initialBalance)
		}
		
		// 即使失败，也尝试设置默认的账户状态（避免前端显示为0）
		record.AccountState = logger.AccountSnapshot{
			TotalBalance:          0,
			AvailableBalance:      0,
			TotalUnrealizedProfit: 0,
			PositionCount:         0,
			MarginUsedPct:         0,
		}
		return fmt.Errorf("构建交易上下文失败: %w", err)
	}

	// 2.6. 同步手动交易到历史记录 - 在每次AI周期开始时检查是否有手动平仓
	// 这样可以确保手动平仓被正确记录到交易历史中
	if err := at.SyncManualTradesFromExchange(); err != nil {
		log.Printf("⚠️  同步手动交易失败: %v", err)
		// 即使同步失败也不影响主要流程
	}

	// 2.7. 重置日盈亏（每天重置）- 需要账户数据来计算
	if needResetDailyPnL {
		// 记录今日开盘时的净值（用于计算日盈亏）
		at.riskMu.Lock()
		at.dailyStartEquity = ctx.Account.TotalEquity
		at.dailyPnL = 0
		// ⚠️ 峰值净值不应该重置！应该在整个交易期间保持（直到超过）
		// 如果当前净值超过峰值，更新峰值（这种情况在checkAndExecuteForcedStopLoss中也会检查）
		if ctx.Account.TotalEquity > at.peakEquity {
			at.peakEquity = ctx.Account.TotalEquity
		}
		peakEquitySnapshot := at.peakEquity
		dailyStartEquitySnapshot := at.dailyStartEquity
		at.riskMu.Unlock()
		at.lastResetTime = time.Now()
		log.Printf("📅 日盈亏已重置，今日开盘净值: %.2f USDT (峰值净值: %.2f USDT)", 
			dailyStartEquitySnapshot, peakEquitySnapshot)
	}

	// 3. 清理已强制平仓的持仓记录（新周期开始）
	// 优化：只清理已不存在的持仓，而不是清空整个map
	// 这样可以在AI周期中间被独立检查标记的持仓保持标记状态
	currentPositionKeys := make(map[string]bool)
	for _, pos := range ctx.Positions {
		posKey := pos.Symbol + "_" + pos.Side
		currentPositionKeys[posKey] = true
	}
	
	at.forcedCloseMu.Lock()
	// 清理已不存在的持仓标记，以及超过5分钟的失败标记（允许重试）
	for key := range at.forcedClosedPositions {
		if !currentPositionKeys[key] {
			// 如果持仓已不存在，检查是否是失败标记且超过重试超时时间
			markTime := at.forcedClosedPositions[key]
			if time.Since(markTime) > PositionStopLossRetryTimeout {
				// 超过5分钟，允许重试，删除标记
				delete(at.forcedClosedPositions, key)
			} else {
				// 持仓不存在但标记未过期，保留标记（可能是刚平仓）
				// 但在下次检查时会因为持仓不存在而清理
			}
		}
	}
	at.forcedCloseMu.Unlock()

	// 4. 执行强制止损检查（在AI决策之前）
	forcedActions, err := at.checkAndExecuteForcedStopLoss(ctx)
	if err != nil {
		log.Printf("⚠️  强制止损检查失败: %v", err)
		// 不影响主流程，继续执行AI决策
	}

	// 记录强制平仓的操作
	for _, action := range forcedActions {
		record.Decisions = append(record.Decisions, action)
		record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("🛑 强制平仓: %s %s - %s", action.Symbol, action.Action, action.ForcedReason))
		
		// 清理已强制平仓的持仓时间记录
		posKey := action.Symbol + "_" + strings.ToLower(strings.TrimPrefix(action.Action, "close_"))
		at.positionTimeMu.Lock()
		delete(at.positionFirstSeenTime, posKey)
		at.positionTimeMu.Unlock()
		// 持仓时间已直接保存到数据库，无需批量保存
	}

	// 如果强制平仓后需要更新账户和持仓状态（因为持仓已变化）
	if len(forcedActions) > 0 {
		log.Printf("🔄 强制平仓后重新构建交易上下文...")
		// 重新构建完整上下文，确保数据一致性
		var rebuildErr error
		ctx, rebuildErr = at.buildTradingContext()
		if rebuildErr != nil {
			log.Printf("⚠️  强制平仓后重新构建上下文失败: %v，使用部分更新作为fallback", rebuildErr)
			// 如果重建失败，使用部分更新作为fallback
			balance, err := at.trader.GetBalance()
			if err == nil {
				totalWalletBalance := 0.0
				totalUnrealizedProfit := 0.0
				availableBalance := 0.0
				if wallet, ok := balance["totalWalletBalance"].(float64); ok {
					totalWalletBalance = wallet
				}
				if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
					totalUnrealizedProfit = unrealized
				}
				if avail, ok := balance["availableBalance"].(float64); ok {
					availableBalance = avail
				}
				totalEquity := totalWalletBalance + totalUnrealizedProfit
				totalPnL := totalEquity - at.initialBalance
				totalPnLPct := 0.0
				if at.initialBalance > 0 {
					totalPnLPct = (totalPnL / at.initialBalance) * 100
				}
				
				// 更新账户信息
				ctx.Account.TotalEquity = totalEquity
				ctx.Account.AvailableBalance = availableBalance
				ctx.Account.TotalPnL = totalPnL
				ctx.Account.TotalPnLPct = totalPnLPct
			}
			
			// 更新持仓列表
			positions, err := at.trader.GetPositions()
			if err == nil {
				var positionInfos []decision.PositionInfo
				totalMarginUsed := 0.0
				currentPositionKeys := make(map[string]bool)
				
				for _, pos := range positions {
				symbol := pos["symbol"].(string)
				side := pos["side"].(string)
				entryPrice := pos["entryPrice"].(float64)
				markPrice := pos["markPrice"].(float64)
				quantity := pos["positionAmt"].(float64)
				if quantity < 0 {
					quantity = -quantity
				}
				unrealizedPnl := pos["unRealizedProfit"].(float64)
				liquidationPrice := pos["liquidationPrice"].(float64)
				
				leverage := 10
				if lev, ok := pos["leverage"].(float64); ok {
					leverage = int(lev)
				}
				marginUsed := (quantity * markPrice) / float64(leverage)
				totalMarginUsed += marginUsed
				
				pnlPct := 0.0
				if side == "long" {
					pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
				} else {
					pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
				}
				
				posKey := symbol + "_" + side
				currentPositionKeys[posKey] = true
				
				// 获取持仓时间（如果存在）
				updateTime := int64(0)
				at.positionTimeMu.RLock()
				if timeVal, exists := at.positionFirstSeenTime[posKey]; exists {
					updateTime = timeVal
				}
				at.positionTimeMu.RUnlock()
				
				// 从PositionLogicManager读取止损/止盈价格（与逻辑一起持久化）
				var stopLoss, takeProfit float64
				logic := at.positionLogicManager.GetLogic(symbol, side)
				if logic != nil {
					stopLoss = logic.StopLoss
					takeProfit = logic.TakeProfit
					// 调试日志：确认读取到的止损止盈值
					if stopLoss > 0 || takeProfit > 0 {
						log.Printf("  📌 [%s %s] 从PositionLogicManager读取: 止损=%.4f, 止盈=%.4f", symbol, side, stopLoss, takeProfit)
					}
				}
				
				positionInfos = append(positionInfos, decision.PositionInfo{
					Symbol:           symbol,
					Side:             side,
					EntryPrice:       entryPrice,
					MarkPrice:        markPrice,
					Quantity:         quantity,
					Leverage:         leverage,
					UnrealizedPnL:    unrealizedPnl,
					UnrealizedPnLPct: pnlPct,
					LiquidationPrice: liquidationPrice,
					MarginUsed:       marginUsed,
					UpdateTime:       updateTime,
					StopLoss:         stopLoss,
					TakeProfit:       takeProfit,
				})
			}
			
			// 更新持仓列表
			ctx.Positions = positionInfos
			ctx.Account.PositionCount = len(positionInfos)
			
			// 更新保证金使用率
			marginUsedPct := 0.0
			if ctx.Account.TotalEquity > 0 {
				marginUsedPct = (totalMarginUsed / ctx.Account.TotalEquity) * 100
			}
			ctx.Account.MarginUsed = totalMarginUsed
			ctx.Account.MarginUsedPct = marginUsedPct
			
			// 检测并处理已平仓的持仓（包括手动平仓），记录到交易历史
			at.positionTimeMu.Lock()
			var closedPositions []string
			for key := range at.positionFirstSeenTime {
				if !currentPositionKeys[key] {
					closedPositions = append(closedPositions, key)
				}
			}
			at.positionTimeMu.Unlock()
			
			// 为每个已平仓的持仓构建交易记录并保存
			for _, posKey := range closedPositions {
				// 解析持仓键为symbol和side
				parts := strings.Split(posKey, "_")
				if len(parts) < 2 {
					// 清理该持仓记录
					at.positionTimeMu.Lock()
					delete(at.positionFirstSeenTime, posKey)
					at.positionTimeMu.Unlock()
					continue
				}
				
				symbol := parts[0]
				side := parts[1]
				
				// 先获取开仓时间（在删除记录之前）
				at.positionTimeMu.RLock()
				openTimeMs, exists := at.positionFirstSeenTime[posKey]
				at.positionTimeMu.RUnlock()
				
				if !exists {
					log.Printf("⚠️  无法获取 %s 的开仓时间", posKey)
					// 清理持仓记录
					at.positionTimeMu.Lock()
					delete(at.positionFirstSeenTime, posKey)
					at.positionTimeMu.Unlock()
					continue
				}
				
				openTime := time.UnixMilli(openTimeMs)
				
				// 尝试从PositionLogicManager获取持仓逻辑，其中可能包含入场价格等信息
				logic := at.positionLogicManager.GetLogic(symbol, side)
				var entryPrice float64
				var leverage int
				var quantity float64
				if logic != nil && logic.EntryLogic != nil {
					// 这里我们需要从其他地方获取入口价格，因为logic结构中可能没有直接的价格信息
					// 先尝试从数据库记录中查询
					entryPrice, quantity, leverage = at.getEntryInfoFromHistory(symbol, side)
				}
				
				// 如果无法从历史中获取入场信息，则跳过记录（或使用估算值）
				if entryPrice == 0 {
					log.Printf("⚠️  无法获取已平仓 %s 的入场信息，尝试从持仓逻辑获取", posKey)
					// 尝试从持仓逻辑中获取更多信息，但目前这些结构可能不包含入场价格
					// 我们可以尝试调用之前实现的同步函数
					log.Printf("ℹ️  建议运行SyncManualTradesFromExchange()来同步手动交易")
					// 清理持仓记录但不记录交易历史
					at.positionTimeMu.Lock()
					delete(at.positionFirstSeenTime, posKey)
					at.positionTimeMu.Unlock()
					continue
				}
				
				// 从交易所获取平仓价格（最准确的方式）
				// 获取最近的交易历史来获取平仓价格
				closePrice, err := at.getLatestClosePrice(symbol, side)
				if err != nil || closePrice == 0 {
					log.Printf("⚠️  无法获取 %s 的平仓价格: %v", posKey, err)
					// 如果无法获取准确的平仓价格，使用当前市场价格作为估算
					marketData, err := market.Get(symbol)
					if err != nil {
						log.Printf("⚠️  获取 %s 市场数据失败: %v", symbol, err)
						// 清理持仓记录但不记录交易历史
						at.positionTimeMu.Lock()
						delete(at.positionFirstSeenTime, posKey)
						at.positionTimeMu.Unlock()
						continue
					}
					closePrice = marketData.CurrentPrice
					log.Printf("📊 使用当前市场价格 %.4f 作为 %s 的平仓价格估算", closePrice, posKey)
				}
				
				// 构建开仓操作记录（从历史中获取或估算）
				openAction := &logger.DecisionAction{
					Symbol:    symbol,
					Action:    fmt.Sprintf("open_%s", side),
					Price:     entryPrice,
					Quantity:  quantity,
					Leverage:  leverage,
					Timestamp: openTime,
					Success:   true,
				}
				
				// 构建平仓操作记录
				closeAction := &logger.DecisionAction{
					Symbol:    symbol,
					Action:    fmt.Sprintf("close_%s", side),
					Price:     closePrice,
					Quantity:  quantity,
					Leverage:  leverage,
					Timestamp: time.Now(), // 使用当前时间作为平仓时间
					Success:   true,
				}
				
				// 构建交易记录
				trade := at.buildTradeRecord(symbol, side, openAction, closeAction, 0, atomic.LoadInt64(&at.callCount), false, "", "系统外开仓", "手动平仓")
				
				// 保存交易历史到数据库
				if at.storageAdapter != nil {
					tradeStorage := at.storageAdapter.GetTradeStorage()
					if tradeStorage != nil {
						// 转换logger.TradeRecord到storage.TradeRecord
						dbTrade := &storage.TradeRecord{
							TradeID:        trade.TradeID,
							Symbol:         trade.Symbol,
							Side:           trade.Side,
							OpenTime:       trade.OpenTime,
							OpenPrice:      trade.OpenPrice,
							OpenQuantity:   trade.OpenQuantity,
							OpenLeverage:   trade.OpenLeverage,
							OpenOrderID:    trade.OpenOrderID,
							OpenReason:     trade.OpenReason,
							OpenCycleNum:   trade.OpenCycleNum,
							CloseTime:      trade.CloseTime,
							ClosePrice:     trade.ClosePrice,
							CloseQuantity:  trade.CloseQuantity,
							CloseOrderID:   trade.CloseOrderID,
							CloseReason:    trade.CloseReason,
							CloseCycleNum:  trade.CloseCycleNum,
							IsForced:       trade.IsForced,
							ForcedReason:   trade.ForcedReason,
							Duration:       trade.Duration,
							PositionValue:  trade.PositionValue,
							MarginUsed:     trade.MarginUsed,
							PnL:            trade.PnL,
							PnLPct:         trade.PnLPct,
							WasStopLoss:    trade.WasStopLoss,
							Success:        trade.Success,
							Error:          trade.Error,
						}
						
						if err := tradeStorage.LogTrade(dbTrade); err != nil {
							log.Printf("⚠️  保存手动平仓历史到数据库失败: %v", err)
						} else {
							log.Printf("✅ 已记录手动平仓历史: %s_%s, 盈亏: %.2f USDT (%.2f%%)", symbol, side, trade.PnL, trade.PnLPct)
						}
					}
				}
				
				// 从缓存中清理已处理的持仓记录
				at.positionTimeMu.Lock()
				delete(at.positionFirstSeenTime, posKey)
				at.positionTimeMu.Unlock()
				
				// 同时删除持仓逻辑
				if at.positionLogicManager != nil {
					if err := at.positionLogicManager.DeleteLogic(symbol, side); err != nil {
						log.Printf("⚠️  删除持仓逻辑失败 %s: %v", posKey, err)
					}
				}
			}
			}
		} else {
			log.Printf("✓ 强制平仓后上下文已重新构建")
		}
	}

	// 在强制平仓后统一保存账户和持仓快照（确保数据一致性）
	record.AccountState = logger.AccountSnapshot{
		TotalBalance:          ctx.Account.TotalEquity,
		AvailableBalance:      ctx.Account.AvailableBalance,
		TotalUnrealizedProfit: ctx.Account.TotalPnL,
		PositionCount:         ctx.Account.PositionCount,
		MarginUsedPct:         ctx.Account.MarginUsedPct,
	}

	// 保存持仓快照（使用更新后的持仓列表）
	for _, pos := range ctx.Positions {
		record.Positions = append(record.Positions, logger.PositionSnapshot{
			Symbol:           pos.Symbol,
			Side:             pos.Side,
			PositionAmt:      pos.Quantity,
			EntryPrice:       pos.EntryPrice,
			MarkPrice:        pos.MarkPrice,
			UnrealizedProfit: pos.UnrealizedPnL,
			Leverage:         float64(pos.Leverage),
			LiquidationPrice: pos.LiquidationPrice,
		})
	}

	// 保存候选币种列表
	for _, coin := range ctx.CandidateCoins {
		record.CandidateCoins = append(record.CandidateCoins, coin.Symbol)
	}

	log.Printf("📊 账户净值: %.2f USDT | 可用: %.2f USDT | 持仓: %d",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance, ctx.Account.PositionCount)

	// 4. 调用AI获取完整决策
	log.Println("🤖 正在请求AI分析并决策...")
	decision, err := decision.GetFullDecision(ctx, at.mcpClient)

	// 即使有错误，也保存思维链、决策和输入prompt（用于debug）
	if decision != nil {
		record.InputPrompt = decision.UserPrompt
		record.CoTTrace = decision.CoTTrace
		if len(decision.Decisions) > 0 {
			decisionJSON, _ := json.MarshalIndent(decision.Decisions, "", "  ")
			record.DecisionJSON = string(decisionJSON)
		}
	}

	if err != nil {
		record.Success = false
		record.ErrorMessage = fmt.Sprintf("获取AI决策失败: %v", err)

		// 打印AI思维链（即使有错误）
		if decision != nil && decision.CoTTrace != "" {
			log.Printf("\n" + strings.Repeat("-", 70))
			log.Println("💭 AI思维链分析（错误情况）:")
			log.Println(strings.Repeat("-", 70))
			log.Println(decision.CoTTrace)
			log.Printf(strings.Repeat("-", 70) + "\n")
		}

		return fmt.Errorf("获取AI决策失败: %w", err)
	}

	// 5. 打印AI思维链
	log.Printf("\n" + strings.Repeat("-", 70))
	log.Println("💭 AI思维链分析:")
	log.Println(strings.Repeat("-", 70))
	log.Println(decision.CoTTrace)
	log.Printf(strings.Repeat("-", 70) + "\n")

	// 6. 打印AI决策
	log.Printf("📋 AI决策列表 (%d 个):\n", len(decision.Decisions))
	for i, d := range decision.Decisions {
		log.Printf("  [%d] %s: %s - %s", i+1, d.Symbol, d.Action, d.Reasoning)
		if d.Action == "open_long" || d.Action == "open_short" {
			log.Printf("      杠杆: %dx | 仓位: %.2f USDT | 止损: %.4f | 止盈: %.4f",
				d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
		}
	}
	log.Println()

	// 7. 对决策排序：确保先平仓后开仓（防止仓位叠加超限）
	sortedDecisions := sortDecisionsByPriority(decision.Decisions)

	// 7.5. 去重：合并同一币种相同类型的操作（只保留最后一个）
	// 特别针对 update_sl 和 update_tp，避免同一周期内多次更新
	deduplicatedDecisions := deduplicateDecisions(sortedDecisions)

	if len(deduplicatedDecisions) < len(sortedDecisions) {
		log.Printf("🔄 决策去重: %d 个决策 -> %d 个（已合并重复的 update_sl/update_tp 操作）", 
			len(sortedDecisions), len(deduplicatedDecisions))
	}

	log.Println("🔄 执行顺序（已优化）: 先平仓→后开仓")
	for i, d := range deduplicatedDecisions {
		log.Printf("  [%d] %s %s", i+1, d.Symbol, d.Action)
	}
	log.Println()

	// 执行决策并记录结果
	for _, d := range deduplicatedDecisions {
		// 检查是否已被强制平仓
		posKey := d.Symbol + "_" + strings.ToLower(strings.TrimPrefix(d.Action, "close_"))
		at.forcedCloseMu.RLock()
		markTime, isForcedClosed := at.forcedClosedPositions[posKey]
		at.forcedCloseMu.RUnlock()
		if isForcedClosed {
			// 如果是失败标记且超过重试超时时间，允许重试
			if time.Since(markTime) > PositionStopLossRetryTimeout {
				// 超过5分钟，清除标记并允许重试
				at.forcedCloseMu.Lock()
				delete(at.forcedClosedPositions, posKey)
				at.forcedCloseMu.Unlock()
				log.Printf("🔄 %s %s 失败标记已过期（超过%.0f分钟），允许重试", d.Symbol, d.Action, PositionStopLossRetryTimeout.Minutes())
			} else {
				log.Printf("⏭️  跳过 %s %s（已被强制平仓，标记时间: %v）", d.Symbol, d.Action, markTime.Format("15:04:05"))
				record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("⏭️  跳过 %s %s（已被强制平仓）", d.Symbol, d.Action))
				continue
			}
		}

		actionRecord := logger.DecisionAction{
			Action:      d.Action,
			Symbol:      d.Symbol,
			Quantity:    0,
			Leverage:    d.Leverage,
			Price:       0,
			Timestamp:   time.Now(),
			Success:     false,
			IsForced:    false,
			ForcedReason: "",
		}

		if err := at.executeDecisionWithRecord(&d, &actionRecord); err != nil {
			log.Printf("❌ 执行决策失败 (%s %s): %v", d.Symbol, d.Action, err)
			actionRecord.Error = err.Error()
			record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("❌ %s %s 失败: %v", d.Symbol, d.Action, err))
			
			// 如果是平仓失败，记录严重警告（可能导致仓位残留）
			if strings.HasPrefix(d.Action, "close_") {
				log.Printf("⚠️  严重警告：%s %s 平仓失败，可能导致仓位残留！请手动检查", d.Symbol, d.Action)
				record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("⚠️  严重警告：%s %s 平仓失败，可能导致仓位残留", d.Symbol, d.Action))
			}
			// 注意：仍然继续执行后续决策，因为其他决策可能是独立的
			// 但如果需要严格按顺序执行，可以考虑根据错误类型决定是否停止
		} else {
			actionRecord.Success = true
			// 检查是否是跳过操作（通过Error字段中的"SKIPPED:"前缀判断）
			if actionRecord.Error != "" && strings.HasPrefix(actionRecord.Error, "SKIPPED:") {
				skipMsg := strings.TrimPrefix(actionRecord.Error, "SKIPPED: ")
				record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("⏭️  %s %s 已跳过：%s", d.Symbol, d.Action, skipMsg))
			} else {
				record.ExecutionLog = append(record.ExecutionLog, fmt.Sprintf("✓ %s %s 成功", d.Symbol, d.Action))
				// 成功执行后短暂延迟
				time.Sleep(1 * time.Second)
			}
		}

		record.Decisions = append(record.Decisions, actionRecord)
	}

	// 8. 保存决策记录到数据库
	if at.storageAdapter != nil {
		decisionStorage := at.storageAdapter.GetDecisionStorage()
		if decisionStorage != nil {
			// 转换logger.DecisionRecord到storage.DecisionRecord
			accountStateJSON, _ := json.Marshal(record.AccountState)
			positionsJSON, _ := json.Marshal(record.Positions)
			candidateCoinsJSON, _ := json.Marshal(record.CandidateCoins)
			decisionsJSON, _ := json.Marshal(record.Decisions)
			executionLogJSON, _ := json.Marshal(record.ExecutionLog)

			dbRecord := &storage.DecisionRecord{
				Timestamp:      record.Timestamp,
				CycleNumber:    record.CycleNumber,
				InputPrompt:    record.InputPrompt,
				CoTTrace:       record.CoTTrace,
				DecisionJSON:   record.DecisionJSON,
				AccountState:   accountStateJSON,
				Positions:      positionsJSON,
				CandidateCoins: candidateCoinsJSON,
				Decisions:      decisionsJSON,
				ExecutionLog:   executionLogJSON,
				Success:        record.Success,
				ErrorMessage:   record.ErrorMessage,
			}

			if err := decisionStorage.LogDecision(at.id, dbRecord); err != nil {
				log.Printf("⚠️  保存决策记录到数据库失败: %v", err)
			}
		}
	}

	// 9. 记录周期快照（用于自检式review）
	if err := at.logCycleSnapshot(ctx, decision, record, cycleNum); err != nil {
		log.Printf("⚠️  记录周期快照失败: %v", err)
		// 不影响主流程，继续执行
	}

	return nil
}

// buildTradingContext 构建交易上下文
func (at *AutoTrader) buildTradingContext() (*decision.Context, error) {
	// 1. 获取账户信息
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取账户余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	} else {
		log.Printf("⚠️  警告：无法获取totalWalletBalance（类型断言失败），使用默认值0.0")
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	} else {
		log.Printf("⚠️  警告：无法获取totalUnrealizedProfit（类型断言失败），使用默认值0.0")
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	} else {
		log.Printf("⚠️  警告：无法获取availableBalance（类型断言失败），使用默认值0.0")
	}

	// 检查关键字段是否获取成功
	if totalWalletBalance == 0.0 && totalUnrealizedProfit == 0.0 {
		log.Printf("⚠️  严重警告：账户余额和未实现盈亏都为0，可能是数据格式问题！请检查交易所API返回格式")
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 2. 获取持仓信息
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var positionInfos []decision.PositionInfo
	totalMarginUsed := 0.0

	// 当前持仓的key集合（用于清理已平仓的记录）
	currentPositionKeys := make(map[string]bool)

	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity // 空仓数量为负，转为正数
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		// 计算占用保证金（估算）
		leverage := 10 // 默认值，实际应该从持仓信息获取
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed

		// 计算盈亏百分比
		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// 跟踪持仓首次出现时间（只读取已存在的记录，不自动创建）
		// 注意：新持仓的时间应该在实际开仓成功时记录（executeOpenLongWithRecord/executeOpenShortWithRecord）
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true
		updateTime := int64(0)
		at.positionTimeMu.RLock()
		timeVal, exists := at.positionFirstSeenTime[posKey]
		at.positionTimeMu.RUnlock()
		
		if exists {
			updateTime = timeVal
		} else {
			// 如果缓存中没有记录但持仓存在，可能是从交易所直接开仓的，尝试从数据库或日志中查找
			// 首先尝试从数据库获取（如果持仓逻辑已存在）
			if at.positionLogicManager != nil {
				if dbTime, exists := at.positionLogicManager.GetFirstSeenTime(symbol, side); exists && dbTime > 0 {
					updateTime = dbTime
					at.positionTimeMu.Lock()
					at.positionFirstSeenTime[posKey] = updateTime
					at.positionTimeMu.Unlock()
					log.Printf("  📅 从数据库恢复持仓时间: %s %s (开仓时间: %s)", symbol, side, time.UnixMilli(updateTime).Format("15:04:05"))
				} else if foundTime, err := at.findPositionOpenTimeFromLogs(symbol, side); err == nil {
					updateTime = foundTime
					at.positionTimeMu.Lock()
					at.positionFirstSeenTime[posKey] = updateTime
					at.positionTimeMu.Unlock()
					// 保存到数据库
					if err := at.positionLogicManager.SaveFirstSeenTime(symbol, side, updateTime); err != nil {
						log.Printf("⚠️  保存恢复的持仓时间失败: %v", err)
					}
					log.Printf("  📅 从日志恢复持仓时间: %s %s (开仓时间: %s)", symbol, side, time.UnixMilli(updateTime).Format("15:04:05"))
				}
			}
		}

		// 加载持仓逻辑并检查是否失效
		logic := at.positionLogicManager.GetLogic(symbol, side)
		logicInvalid := false
		var invalidReasons []string
		
		if logic != nil {
			// 获取市场数据用于检查逻辑
			if marketData, err := market.Get(symbol); err == nil {
				// 构建完整的上下文，确保逻辑检查有足够的数据
				ctx := &decision.Context{
					MultiTimeframeConfig: at.config.MultiTimeframeConfig,
					MarketDataMap:        make(map[string]*market.Data),
					StrategyName:         at.config.StrategyName,
					StrategyPreference:   at.config.StrategyPreference,
				}
				// 将市场数据放入上下文，以便逻辑检查可以访问
				ctx.MarketDataMap[symbol] = marketData
				logicInvalid, invalidReasons = decision.CheckLogicValidity(logic, symbol, marketData, ctx, side)
			}
		}
		
		// 从PositionLogicManager读取止损/止盈价格（与逻辑一起持久化，已经在上面获取了logic）
		var stopLoss, takeProfit float64
		if logic != nil {
			stopLoss = logic.StopLoss
			takeProfit = logic.TakeProfit
			// 调试日志：确认读取到的止损止盈值
			if stopLoss > 0 || takeProfit > 0 {
				log.Printf("  📌 [%s %s] 从PositionLogicManager读取: 止损=%.4f, 止盈=%.4f", symbol, side, stopLoss, takeProfit)
			}
		}
		
		positionInfo := decision.PositionInfo{
			Symbol:           symbol,
			Side:             side,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			Quantity:         quantity,
			Leverage:         leverage,
			UnrealizedPnL:    unrealizedPnl,
			UnrealizedPnLPct: pnlPct,
			LiquidationPrice: liquidationPrice,
			MarginUsed:       marginUsed,
			UpdateTime:       updateTime,
			StopLoss:         stopLoss,
			TakeProfit:       takeProfit,
		}
		
		// 设置逻辑信息
		if logic != nil {
			positionInfo.EntryLogic = logic.EntryLogic
			positionInfo.ExitLogic = logic.ExitLogic
		}
		positionInfo.LogicInvalid = logicInvalid
		positionInfo.InvalidReasons = invalidReasons
		
		positionInfos = append(positionInfos, positionInfo)
	}

	// 清理已平仓的持仓记录（包括时间和止损/止盈价格）
	at.positionTimeMu.Lock()
	for key := range at.positionFirstSeenTime {
		if !currentPositionKeys[key] {
			delete(at.positionFirstSeenTime, key)
		}
	}
	at.positionTimeMu.Unlock()
	
	// 清理已平仓的止损/止盈价格（通过PositionLogicManager删除逻辑，会自动清理止损/止盈）
	// PositionLogicManager会在DeleteLogic时自动清理，这里不需要额外操作

	// 3. 获取候选币种池
	// 无论有没有持仓，都分析相同数量的币种（让AI看到所有好机会）
	// AI会根据保证金使用率和现有持仓情况，自己决定是否要换仓
	const coinLimit = 20 // 取前20个评分最高的币种

	// 获取币种池
	mergedPool, err := pool.GetMergedCoinPool(coinLimit)
	if err != nil {
		return nil, fmt.Errorf("获取币种池失败: %w", err)
	}

	// 构建候选币种列表（包含来源信息）
	var candidateCoins []decision.CandidateCoin
	for _, symbol := range mergedPool.AllSymbols {
		sources := mergedPool.SymbolSources[symbol]
		candidateCoins = append(candidateCoins, decision.CandidateCoin{
			Symbol:  symbol,
			Sources: sources,
		})
	}

	log.Printf("📋 候选币种池: 总计%d个候选币种", len(candidateCoins))

	// 4. 计算总盈亏
	totalPnL := totalEquity - at.initialBalance
	totalPnLPct := 0.0
	if at.initialBalance > 0 {
		totalPnLPct = (totalPnL / at.initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	// 5. 分析历史表现（从数据库获取）
	var performance interface{} = nil
	if at.storageAdapter != nil {
		decisionStorage := at.storageAdapter.GetDecisionStorage()
		if decisionStorage != nil {
			records, err := decisionStorage.GetLatestRecords(at.id, 100)
			if err == nil && len(records) > 0 {
				// 使用数据库记录分析历史表现
				performance = at.analyzePerformanceFromDB(records)
				if performance != nil {
					if perf, ok := performance.(*logger.PerformanceAnalysis); ok {
						log.Printf("📊 已计算Performance数据: 夏普比率=%.2f, 总交易数=%d", perf.SharpeRatio, perf.TotalTrades)
					}
				}
			} else {
				log.Printf("ℹ️  没有足够的决策记录来计算Performance (错误: %v, 记录数: %d)", err, len(records))
			}
		} else {
			log.Printf("ℹ️  DecisionStorage为空，无法计算Performance")
		}
	} else {
		log.Printf("ℹ️  StorageAdapter为空，无法计算Performance")
	}

	// 5.5. 获取最近的强制平仓记录（让AI知道刚刚发生了什么）
	recentForcedCloses := at.getRecentForcedCloses(3) // 最近3个周期的强制平仓记录

	// 6. 构建上下文
	ctx := &decision.Context{
		CurrentTime:     time.Now().Format("2006-01-02 15:04:05"),
		RuntimeMinutes:  int(time.Since(at.startTime).Minutes()),
		CallCount:       int(atomic.LoadInt64(&at.callCount)),
		BTCETHLeverage:  at.config.BTCETHLeverage,  // 使用配置的杠杆倍数
		AltcoinLeverage: at.config.AltcoinLeverage, // 使用配置的杠杆倍数
		Account: decision.AccountInfo{
			TotalEquity:      totalEquity,
			AvailableBalance: availableBalance,
			TotalPnL:         totalPnL,
			TotalPnLPct:      totalPnLPct,
			MarginUsed:       totalMarginUsed,
			MarginUsedPct:    marginUsedPct,
			PositionCount:    len(positionInfos),
		},
		Positions:      positionInfos,
		CandidateCoins: candidateCoins,
		Performance:    performance, // 添加历史表现分析
		RecentForcedCloses: recentForcedCloses, // 最近的强制平仓记录
		SkipLiquidityCheck: at.config.SkipLiquidityCheck, // 是否跳过流动性检查
		AnalysisMode:    at.config.AnalysisMode, // 分析模式
		MultiTimeframeConfig: at.config.MultiTimeframeConfig, // 多时间框架配置
		StrategyName:    at.config.StrategyName, // 策略名称
		StrategyPreference: at.config.StrategyPreference, // 策略偏好
	}

	return ctx, nil
}

// getRecentForcedCloses 获取最近的强制平仓记录（用于AI决策参考）
func (at *AutoTrader) getRecentForcedCloses(maxCycles int) []string {
	if at.storageAdapter == nil {
		return nil
	}

	decisionStorage := at.storageAdapter.GetDecisionStorage()
	if decisionStorage == nil {
		return nil
	}

	forcedCloses, err := decisionStorage.GetForcedCloses(at.id, maxCycles)
	if err != nil {
		log.Printf("⚠️  获取强制平仓记录失败: %v", err)
		return nil
	}

	return forcedCloses
}

// findPositionOpenTimeFromLogs 从数据库查找持仓的开仓时间
func (at *AutoTrader) findPositionOpenTimeFromLogs(symbol, side string) (int64, error) {
	// 首先尝试从内存缓存获取
	posKey := symbol + "_" + side
	at.positionTimeMu.RLock()
	if timeVal, exists := at.positionFirstSeenTime[posKey]; exists {
		at.positionTimeMu.RUnlock()
		return timeVal, nil
	}
	at.positionTimeMu.RUnlock()

	// 如果内存中没有，尝试从数据库获取
	if at.positionLogicManager != nil {
		if dbTime, exists := at.positionLogicManager.GetFirstSeenTime(symbol, side); exists && dbTime > 0 {
			// 更新内存缓存
			at.positionTimeMu.Lock()
			at.positionFirstSeenTime[posKey] = dbTime
			at.positionTimeMu.Unlock()
			return dbTime, nil
		}
	}

	return 0, fmt.Errorf("未找到持仓 %s 的开仓时间", posKey)
}

// checkAndExecuteForcedStopLoss 检查并执行强制止损（账户级别风控）
// 注意：单仓位止损检查已移至独立的每分钟检查循环（checkPositionStopLossOnly）
func (at *AutoTrader) checkAndExecuteForcedStopLoss(ctx *decision.Context) ([]logger.DecisionAction, error) {
	var forcedActions []logger.DecisionAction

	// 更新峰值净值和日盈亏（使用锁保护）
	at.riskMu.Lock()
	if ctx.Account.TotalEquity > at.peakEquity {
		at.peakEquity = ctx.Account.TotalEquity
	}

	// 更新日盈亏（每天重置后的累计盈亏）
	// 日盈亏 = 当前净值 - 今日开盘净值
	if time.Since(at.lastResetTime) < 24*time.Hour {
		// 在同一天内，日盈亏 = 当前净值 - 今日开盘净值
		at.dailyPnL = ctx.Account.TotalEquity - at.dailyStartEquity
	}
	
	// 读取当前值用于后续计算
	currentPeakEquity := at.peakEquity
	currentDailyPnL := at.dailyPnL
	currentDailyStartEquity := at.dailyStartEquity
	at.riskMu.Unlock()

	// 1. 检查账户级别风控（优先级最高）
	// 检查最大回撤
	if at.config.MaxDrawdown > 0 && currentPeakEquity > 0 {
		currentDrawdown := ((currentPeakEquity - ctx.Account.TotalEquity) / currentPeakEquity) * 100
		if currentDrawdown > at.config.MaxDrawdown {
			// 计算账户总盈亏百分比（相对初始余额）
			totalPnLPct := ctx.Account.TotalPnLPct
			log.Printf("🛑 触发账户回撤风控: 当前回撤%.2f%% > 最大回撤%.2f%%，账户总盈亏%.2f%% (%.2f USDT)，暂停交易%.0f分钟",
				currentDrawdown, at.config.MaxDrawdown, totalPnLPct, ctx.Account.TotalPnL, at.config.StopTradingTime.Minutes())
			
			// 设置暂停交易时间
			at.stopUntil = time.Now().Add(at.config.StopTradingTime)
			
			// 强制平掉所有持仓
			log.Printf("🛑 回撤风控触发：强制平掉所有持仓")
			allForced, err := at.forceCloseAllPositions("账户回撤风控", ctx)
			if err != nil {
				return forcedActions, fmt.Errorf("强制平掉所有持仓失败: %w", err)
			}
			forcedActions = append(forcedActions, allForced...)
			
			return forcedActions, nil
		}
	}

	// 检查最大日亏损
	// 使用当日开盘净值作为分母，更符合"当日亏损百分比"的定义
	if at.config.MaxDailyLoss > 0 && currentDailyStartEquity > 0 {
		dailyLossPct := (currentDailyPnL / currentDailyStartEquity) * 100
		if dailyLossPct < -at.config.MaxDailyLoss {
			// 计算账户总盈亏百分比（相对初始余额）
			totalPnLPct := ctx.Account.TotalPnLPct
			log.Printf("🛑 触发账户日亏损风控: 日亏损%.2f%% > 最大日亏损%.2f%%，账户总盈亏%.2f%% (%.2f USDT)，暂停交易%.0f分钟",
				-dailyLossPct, at.config.MaxDailyLoss, totalPnLPct, ctx.Account.TotalPnL, at.config.StopTradingTime.Minutes())
			
			// 设置暂停交易时间
			at.stopUntil = time.Now().Add(at.config.StopTradingTime)
			
			// 强制平掉所有持仓
			log.Printf("🛑 日亏损风控触发：强制平掉所有持仓")
			allForced, err := at.forceCloseAllPositions("账户日亏损风控", ctx)
			if err != nil {
				return forcedActions, fmt.Errorf("强制平掉所有持仓失败: %w", err)
			}
			forcedActions = append(forcedActions, allForced...)
			
			return forcedActions, nil
		}
	}

	// 注意：单仓位止损检查已移至独立的每分钟检查循环（checkPositionStopLossOnly）
	// 这里只保留账户级别的风控检查

	if len(forcedActions) > 0 {
		log.Printf("🛑 本周期强制平仓 %d 个持仓", len(forcedActions))
	}

	return forcedActions, nil
}

// checkPositionStopLossOnly 检查单仓位止损和止盈（每10秒执行，不依赖scan_interval_minutes）
// 这个函数独立运行，不需要调用AI，专门用于快速响应市场变化（包括插针行情）
// 如果配置了position_take_profit_pct > 0，也会检查强制止盈
// 使用市价单全平，确保快速执行
func (at *AutoTrader) checkPositionStopLossOnly() {
	// 检查是否在运行
	if atomic.LoadInt32(&at.isRunning) == 0 {
		return
	}

	// 获取账户信息和持仓信息（用于构建日志记录）
	balance, err := at.trader.GetBalance()
	if err != nil {
		log.Printf("⚠️  单仓位止损检查：获取账户信息失败: %v", err)
		// 继续执行，即使账户信息获取失败
	}

	// 获取持仓信息（轻量级检查，不需要构建完整上下文）
	positions, err := at.trader.GetPositions()
	if err != nil {
		log.Printf("⚠️  单仓位止损检查：获取持仓失败: %v", err)
		return
	}

	// 如果没有任何持仓，直接返回
	if len(positions) == 0 {
		return
	}

	// 构建当前持仓的key集合（用于后续记录）
	currentPositionKeys := make(map[string]bool)
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		posKey := symbol + "_" + side
		currentPositionKeys[posKey] = true
	}

	// 获取单仓位止损配置
	positionStopLossPct := at.config.PositionStopLossPct
	
	// 检查是否使用默认值：如果配置为0，可能是未设置或设为0
	// 需要区分：未设置(0) vs 明确设为0(禁用止损) vs 设为其他值
	if positionStopLossPct == 0 {
		// 如果配置值为0，可能是因为未在配置文件中指定，使用默认的10%
		log.Printf("⚠️  仓位止损百分比未在配置文件中指定，使用默认值: 10.00%%")
		positionStopLossPct = 10.0
	}

	// 遍历所有持仓，检查亏损百分比
	var forcedActions []logger.DecisionAction
	forcedCount := 0
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}

		// 计算盈亏百分比
		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		var pnlPct float64
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		// 检查止损（只检查亏损的持仓）
		if pnlPct < 0 {
			lossPct := -pnlPct // 转为正数
			if lossPct >= positionStopLossPct {
				log.Printf("🛑 [每10秒检查] 触发单仓位强制止损: %s %s 亏损%.2f%% > %.2f%%，市价全平",
					symbol, side, lossPct, positionStopLossPct)

				// 执行强制平仓
				action, err := at.forceClosePosition(symbol, side, fmt.Sprintf("单仓位亏损%.2f%%超过%.2f%%", lossPct, positionStopLossPct))
				if err != nil {
					log.Printf("⚠️  强制平仓失败 (%s %s): %v", symbol, side, err)
					// 失败时也记录到日志中
					forcedActions = append(forcedActions, action)
					continue
				}

				forcedCount++
				forcedActions = append(forcedActions, action)

				// 注意：已强制平仓的标记在 forceClosePosition 函数内部完成（带锁保护）
				// 清理已强制平仓的持仓时间记录
				posKey := symbol + "_" + side
				at.positionTimeMu.Lock()
				delete(at.positionFirstSeenTime, posKey)
				at.positionTimeMu.Unlock()

				log.Printf("  ✓ 强制平仓成功: %s %s - 单仓位亏损%.2f%%", symbol, side, lossPct)
				continue // 已处理止损，继续下一个持仓
			}
		}

		// 检查止盈（如果配置了止盈百分比，且持仓盈利）
		positionTakeProfitPct := at.config.PositionTakeProfitPct
		if positionTakeProfitPct > 0 && pnlPct > 0 {
			profitPct := pnlPct // 已经是正数
			if profitPct >= positionTakeProfitPct {
				log.Printf("🎯 [每10秒检查] 触发单仓位强制止盈: %s %s 盈利%.2f%% >= %.2f%%，市价全平",
					symbol, side, profitPct, positionTakeProfitPct)

				// 执行强制平仓（止盈）
				action, err := at.forceClosePosition(symbol, side, fmt.Sprintf("单仓位盈利%.2f%%达到%.2f%%止盈目标", profitPct, positionTakeProfitPct))
				if err != nil {
					log.Printf("⚠️  强制平仓失败 (%s %s): %v", symbol, side, err)
					// 失败时也记录到日志中
					forcedActions = append(forcedActions, action)
					continue
				}

				forcedCount++
				forcedActions = append(forcedActions, action)

				// 清理已强制平仓的持仓时间记录
				posKey := symbol + "_" + side
				at.positionTimeMu.Lock()
				delete(at.positionFirstSeenTime, posKey)
				at.positionTimeMu.Unlock()

				log.Printf("  ✓ 强制平仓成功（止盈）: %s %s - 单仓位盈利%.2f%%", symbol, side, profitPct)
			}
		}
	}

	// 如果有强制平仓操作，记录到日志中
	if len(forcedActions) > 0 {
		// 计算并显示账户总盈亏百分比（相对初始余额）
		totalPnLPct := 0.0
		totalPnL := 0.0
		if balance != nil {
			totalWalletBalance := 0.0
			totalUnrealizedProfit := 0.0
			if wallet, ok := balance["totalWalletBalance"].(float64); ok {
				totalWalletBalance = wallet
			}
			if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
				totalUnrealizedProfit = unrealized
			}
			totalEquity := totalWalletBalance + totalUnrealizedProfit
			totalPnL = totalEquity - at.initialBalance
			if at.initialBalance > 0 {
				totalPnLPct = (totalPnL / at.initialBalance) * 100
			}
		}
		
		log.Printf("🛑 [每10秒检查] 本周期强制平仓 %d 个持仓（市价全平），当前账户总盈亏: %.2f%% (%.2f USDT)",
			forcedCount, totalPnLPct, totalPnL)
		
		// 构建账户状态快照（用于日志记录）
		var accountState logger.AccountSnapshot
		if balance != nil {
			totalWalletBalance := 0.0
			totalUnrealizedProfit := 0.0
			availableBalance := 0.0
			if wallet, ok := balance["totalWalletBalance"].(float64); ok {
				totalWalletBalance = wallet
			}
			if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
				totalUnrealizedProfit = unrealized
			}
			if avail, ok := balance["availableBalance"].(float64); ok {
				availableBalance = avail
			}
			totalEquity := totalWalletBalance + totalUnrealizedProfit
			totalPnL := totalEquity - at.initialBalance
			
			accountState = logger.AccountSnapshot{
				TotalBalance:          totalEquity,
				AvailableBalance:      availableBalance,
				TotalUnrealizedProfit: totalPnL,
				PositionCount:         len(positions),
			}
		}

		// 构建持仓快照
		var positionSnapshots []logger.PositionSnapshot
		for _, pos := range positions {
			symbol := pos["symbol"].(string)
			side := pos["side"].(string)
			entryPrice := pos["entryPrice"].(float64)
			markPrice := pos["markPrice"].(float64)
			quantity := pos["positionAmt"].(float64)
			if quantity < 0 {
				quantity = -quantity
			}
			unrealizedPnl := pos["unRealizedProfit"].(float64)
			liquidationPrice := pos["liquidationPrice"].(float64)
			
			leverage := 10.0
			if lev, ok := pos["leverage"].(float64); ok {
				leverage = lev
			}

			positionSnapshots = append(positionSnapshots, logger.PositionSnapshot{
				Symbol:           symbol,
				Side:             side,
				PositionAmt:      quantity,
				EntryPrice:       entryPrice,
				MarkPrice:        markPrice,
				UnrealizedProfit: unrealizedPnl,
				Leverage:         leverage,
				LiquidationPrice: liquidationPrice,
			})
		}

		// 构建执行日志
		executionLog := []string{}
		for _, action := range forcedActions {
			if action.Success {
				executionLog = append(executionLog, fmt.Sprintf("🛑 强制平仓: %s %s - %s", action.Symbol, action.Action, action.ForcedReason))
			} else {
				executionLog = append(executionLog, fmt.Sprintf("❌ 强制平仓失败: %s %s - %s (错误: %s)", action.Symbol, action.Action, action.ForcedReason, action.Error))
			}
		}

		// 保存止损检查日志到数据库
		if at.storageAdapter != nil && len(forcedActions) > 0 {
			decisionStorage := at.storageAdapter.GetDecisionStorage()
			if decisionStorage != nil {
				// 转换logger.DecisionRecord到storage.DecisionRecord
				accountStateJSON, _ := json.Marshal(accountState)
				positionsJSON, _ := json.Marshal(positionSnapshots)
				decisionsJSON, _ := json.Marshal(forcedActions)
				executionLogJSON, _ := json.Marshal(executionLog)

				dbRecord := &storage.DecisionRecord{
					Timestamp:      time.Now(),
					CycleNumber:    0, // 止损检查不计算周期
					InputPrompt:    "[单仓位止损检查] 每10秒执行的止损检查，快速响应插针行情，使用市价全平",
					CoTTrace:       "",
					DecisionJSON:   "",
					AccountState:   accountStateJSON,
					Positions:      positionsJSON,
					CandidateCoins: json.RawMessage("[]"),
					Decisions:      decisionsJSON,
					ExecutionLog:   executionLogJSON,
					Success:        true,
					ErrorMessage:   "",
				}

				if err := decisionStorage.LogDecision(at.id, dbRecord); err != nil {
					log.Printf("⚠️  保存止损检查日志到数据库失败: %v", err)
				}
			}
		}
	}
}

// getOrCreateClosingLock 获取或创建某个持仓的平仓锁（防止并发平仓）
func (at *AutoTrader) getOrCreateClosingLock(posKey string) *sync.Mutex {
	at.closingPositionsMu.Lock()
	defer at.closingPositionsMu.Unlock()
	
	if lock, exists := at.closingPositions[posKey]; exists {
		return lock
	}
	
	// 创建新的锁
	lock := &sync.Mutex{}
	at.closingPositions[posKey] = lock
	return lock
}

// cleanupClosingLock 清理已完成的平仓锁
func (at *AutoTrader) cleanupClosingLock(posKey string) {
	at.closingPositionsMu.Lock()
	defer at.closingPositionsMu.Unlock()
	delete(at.closingPositions, posKey)
}

// forceClosePosition 强制平掉单个持仓（带并发保护）
func (at *AutoTrader) forceClosePosition(symbol, side, reason string) (logger.DecisionAction, error) {
	posKey := symbol + "_" + side
	
	// 先检查是否已被标记为强制平仓（快速检查，避免不必要的锁定）
	at.forcedCloseMu.RLock()
	markTime, alreadyForced := at.forcedClosedPositions[posKey]
	at.forcedCloseMu.RUnlock()
	if alreadyForced {
		// 如果是失败标记且超过重试超时时间，允许重试
		if time.Since(markTime) > PositionStopLossRetryTimeout {
			// 超过5分钟，清除标记并允许重试
			at.forcedCloseMu.Lock()
			delete(at.forcedClosedPositions, posKey)
			at.forcedCloseMu.Unlock()
			log.Printf("🔄 %s %s 失败标记已过期（超过%.0f分钟），允许重试", symbol, side, PositionStopLossRetryTimeout.Minutes())
		} else {
			return logger.DecisionAction{}, fmt.Errorf("持仓 %s %s 已被标记为强制平仓（标记时间: %v），跳过", symbol, side, markTime.Format("15:04:05"))
		}
	}
	
	// 获取该持仓的平仓锁（确保同一时间只有一个操作在平这个仓位）
	closingLock := at.getOrCreateClosingLock(posKey)
	closingLock.Lock()
	defer closingLock.Unlock()
	defer at.cleanupClosingLock(posKey) // 平仓完成后清理锁
	
	// 再次检查（双重检查，防止在获取锁的期间被其他goroutine平仓）
	at.forcedCloseMu.RLock()
	markTime, alreadyForced = at.forcedClosedPositions[posKey]
	at.forcedCloseMu.RUnlock()
	if alreadyForced {
		// 如果是失败标记且超过重试超时时间，允许重试
		if time.Since(markTime) > PositionStopLossRetryTimeout {
			// 超过5分钟，清除标记并允许重试
			at.forcedCloseMu.Lock()
			delete(at.forcedClosedPositions, posKey)
			at.forcedCloseMu.Unlock()
			log.Printf("🔄 %s %s 失败标记已过期（超过%.0f分钟），允许重试", symbol, side, PositionStopLossRetryTimeout.Minutes())
		} else {
			return logger.DecisionAction{}, fmt.Errorf("持仓 %s %s 已被标记为强制平仓（标记时间: %v），跳过", symbol, side, markTime.Format("15:04:05"))
		}
	}
	
	// 执行平仓操作
	actionRecord := logger.DecisionAction{
		Action:       "",
		Symbol:       symbol,
		Quantity:     0,
		Leverage:     0,
		Price:        0,
		Timestamp:    time.Now(),
		Success:      false,
		IsForced:     true,
		ForcedReason: reason,
	}

	// 获取当前价格
	marketData, err := market.Get(symbol)
	if err != nil {
		actionRecord.Error = fmt.Sprintf("获取市场数据失败: %v", err)
		return actionRecord, err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 根据方向执行平仓
	var order map[string]interface{}
	if side == "long" {
		actionRecord.Action = "close_long"
		order, err = at.trader.CloseLong(symbol, 0)
	} else {
		actionRecord.Action = "close_short"
		order, err = at.trader.CloseShort(symbol, 0)
	}
	
	if err != nil {
		actionRecord.Error = err.Error()
		// 失败时设置时间戳标记，5分钟后可重试
		at.forcedCloseMu.Lock()
		at.forcedClosedPositions[posKey] = time.Now()
		at.forcedCloseMu.Unlock()
		
		// ⚠️ 严重告警：强制平仓失败可能导致仓位残留风险
		log.Printf("🚨 [严重告警] 强制平仓失败 (%s %s): %v", symbol, side, err)
		log.Printf("🚨 [严重告警] 失败标记已设置（%.0f分钟后可重试），但建议立即手动检查持仓状态", PositionStopLossRetryTimeout.Minutes())
		log.Printf("🚨 [严重告警] 如果持仓仍存在且亏损继续扩大，请立即手动平仓以避免更大损失")
		
		return actionRecord, err
	}
	
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	actionRecord.Success = true
	
	// 标记为已强制平仓（在锁保护下，确保原子性）
	at.forcedCloseMu.Lock()
	at.forcedClosedPositions[posKey] = time.Now()
	at.forcedCloseMu.Unlock()
	
	log.Printf("  ✓ 强制平仓成功: %s %s - %s", symbol, side, reason)
	
	// 清理持仓逻辑（强制平仓后应删除逻辑）
	if err := at.positionLogicManager.DeleteLogic(symbol, side); err != nil {
		log.Printf("  ⚠️  清理持仓逻辑失败: %v", err)
	} else {
		log.Printf("  ✓ 已清理持仓逻辑: %s %s", symbol, side)
	}
	
	// 记录交易历史（从决策记录中查找开仓信息）
	at.recordTradeHistoryFromAction(symbol, side, &actionRecord, true, reason)
	
	return actionRecord, nil
}

// forceCloseAllPositions 强制平掉所有持仓
func (at *AutoTrader) forceCloseAllPositions(reason string, ctx *decision.Context) ([]logger.DecisionAction, error) {
	var actions []logger.DecisionAction

	for _, pos := range ctx.Positions {
		action, err := at.forceClosePosition(pos.Symbol, pos.Side, reason)
		if err != nil {
			log.Printf("⚠️  强制平仓失败 (%s %s): %v", pos.Symbol, pos.Side, err)
			continue
		}
		actions = append(actions, action)
		
		// 记录已强制平仓的持仓
		posKey := pos.Symbol + "_" + pos.Side
		at.forcedCloseMu.Lock()
		at.forcedClosedPositions[posKey] = time.Now()
		at.forcedCloseMu.Unlock()
	}

	return actions, nil
}

// executeDecisionWithRecord 执行AI决策并记录详细信息
func (at *AutoTrader) executeDecisionWithRecord(decision *decision.Decision, actionRecord *logger.DecisionAction) error {
	switch decision.Action {
	case "open_long":
		return at.executeOpenLongWithRecord(decision, actionRecord)
	case "open_short":
		return at.executeOpenShortWithRecord(decision, actionRecord)
	case "close_long":
		return at.executeCloseLongWithRecord(decision, actionRecord)
	case "close_short":
		return at.executeCloseShortWithRecord(decision, actionRecord)
	case "update_tp":
		return at.executeUpdateTakeProfit(decision, actionRecord)
	case "update_sl":
		return at.executeUpdateStopLoss(decision, actionRecord)
	case "hold", "wait":
		// 无需执行，仅记录
		return nil
	default:
		return fmt.Errorf("未知的action: %s", decision.Action)
	}
}

// executeOpenLongWithRecord 执行开多仓并记录详细信息
func (at *AutoTrader) executeOpenLongWithRecord(dec *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📈 开多仓: %s", dec.Symbol)

	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == dec.Symbol && pos["side"] == "long" {
				return fmt.Errorf("❌ %s 已有多仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_long 决策", dec.Symbol)
			}
		}
	}

	// 构建交易上下文用于保证金检查
	ctx, err := at.buildTradingContext()
	if err != nil {
		return fmt.Errorf("构建交易上下文失败: %w", err)
	}

	// 开仓前再次验证保证金（防止在AI决策后保证金发生变化）
	if err := at.checkMarginAndBalanceSafety(ctx, dec); err != nil {
		return fmt.Errorf("保证金检查失败: %w", err)
	}

	// 双重检查：在开仓前再次检查持仓（防止竞态条件）
	positions, err = at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == dec.Symbol && pos["side"] == "long" {
				return fmt.Errorf("❌ 持仓检查失败：在开仓期间检测到已有持仓，可能是并发开仓导致的")
			}
		}
	}

	// 获取当前价格
	marketData, err := market.Get(dec.Symbol)
	if err != nil {
		return err
	}

	// 验证价格有效性（避免除零错误）
	if marketData.CurrentPrice <= 0 {
		return fmt.Errorf("当前价格无效或为0: %.4f", marketData.CurrentPrice)
	}

	// 计算数量（使用最新价格）
	quantity := dec.PositionSizeUSD / marketData.CurrentPrice
	
	// 立即格式化数量到正确精度（避免精度损失）
	formattedQuantityStr, err := at.trader.FormatQuantity(dec.Symbol, quantity)
	if err != nil {
		return fmt.Errorf("格式化数量失败: %w", err)
	}
	formattedQuantity, err := strconv.ParseFloat(formattedQuantityStr, 64)
	if err != nil {
		return fmt.Errorf("解析格式化后的数量失败: %w", err)
	}
	
	// 检查最小数量（使用格式化后的数量）
	minQuantity := MinPositionSizeUSD / marketData.CurrentPrice
	if formattedQuantity < minQuantity {
		return fmt.Errorf("计算出的数量过小(%.8f)，小于最小要求(%.8f)。可能因为仓位大小过小或价格过高", formattedQuantity, minQuantity)
	}

	actionRecord.Quantity = formattedQuantity
	actionRecord.Price = marketData.CurrentPrice

	// 开仓（使用格式化后的数量）
	order, err := at.trader.OpenLong(dec.Symbol, actionRecord.Quantity, dec.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], actionRecord.Quantity)

	// 记录开仓时间
	posKey := dec.Symbol + "_long"
	firstSeenTime := time.Now().UnixMilli()
	at.positionTimeMu.Lock()
	at.positionFirstSeenTime[posKey] = firstSeenTime
	at.positionTimeMu.Unlock()
	// 保存到数据库
	if at.positionLogicManager != nil {
		if err := at.positionLogicManager.SaveFirstSeenTime(dec.Symbol, "long", firstSeenTime); err != nil {
			log.Printf("⚠️  保存持仓首次出现时间失败: %v", err)
		}
	}

	// 设置止损止盈并保存到PositionLogicManager（与逻辑一起持久化）
	if dec.StopLoss > 0 || dec.TakeProfit > 0 {
		// 先保存到PositionLogicManager（无论设置是否成功，都保存AI决策中的价格）
		if err := at.positionLogicManager.SaveStopLossAndTakeProfit(dec.Symbol, "long", dec.StopLoss, dec.TakeProfit); err != nil {
			log.Printf("  ⚠ 保存止损/止盈价格失败: %v", err)
		} else {
			log.Printf("  ✓ 已保存止损/止盈价格到逻辑管理器: 止损=%.4f, 止盈=%.4f", dec.StopLoss, dec.TakeProfit)
		}
		
		// 然后设置到交易所（如果失败不影响已保存的价格）
		if dec.StopLoss > 0 {
			if err := at.trader.SetStopLoss(dec.Symbol, "LONG", quantity, dec.StopLoss); err != nil {
				log.Printf("  ⚠ 设置止损失败: %v (价格已保存到逻辑管理器)", err)
			} else {
				log.Printf("  ✓ 止损设置成功: %.4f", dec.StopLoss)
			}
		}
		if dec.TakeProfit > 0 {
			if err := at.trader.SetTakeProfit(dec.Symbol, "LONG", quantity, dec.TakeProfit); err != nil {
				log.Printf("  ⚠ 设置止盈失败: %v (价格已保存到逻辑管理器)", err)
			} else {
				log.Printf("  ✓ 止盈设置成功: %.4f", dec.TakeProfit)
			}
		}
	}

	// 保存进场逻辑和出场逻辑（复用已获取的市场数据）
	if dec.Reasoning != "" {
		// 构建简化的上下文（只包含必要的市场数据）
		ctx := &decision.Context{
			MultiTimeframeConfig: at.config.MultiTimeframeConfig,
			MarketDataMap:        make(map[string]*market.Data),
		}
		// 复用前面已获取的市场数据，避免重复API调用
		ctx.MarketDataMap[dec.Symbol] = marketData
		
		// 保存进场逻辑
		entryLogic := decision.ExtractEntryLogicFromReasoning(dec.Reasoning, ctx, dec.Symbol)
		if err := at.positionLogicManager.SaveEntryLogic(dec.Symbol, "long", entryLogic); err != nil {
			log.Printf("  ⚠ 保存进场逻辑失败: %v", err)
		} else {
			log.Printf("  ✓ 已保存进场逻辑")
		}
		
		// 保存出场逻辑（如果提供）
		if dec.ExitReasoning != "" {
			exitLogic := decision.ExtractExitLogicFromReasoning(dec.ExitReasoning, ctx, dec.Symbol)
			if err := at.positionLogicManager.SaveExitLogic(dec.Symbol, "long", exitLogic); err != nil {
				log.Printf("  ⚠ 保存出场逻辑失败: %v", err)
			} else {
				log.Printf("  ✓ 已保存出场逻辑")
			}
		} else {
			log.Printf("  ⚠ 警告：开仓时未提供出场逻辑（exit_reasoning），建议在开仓时规划好出场逻辑")
		}
	}

	return nil
}

// executeOpenShortWithRecord 执行开空仓并记录详细信息
func (at *AutoTrader) executeOpenShortWithRecord(dec *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📉 开空仓: %s", dec.Symbol)

	// ⚠️ 关键：检查是否已有同币种同方向持仓，如果有则拒绝开仓（防止仓位叠加超限）
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == dec.Symbol && pos["side"] == "short" {
				return fmt.Errorf("❌ %s 已有空仓，拒绝开仓以防止仓位叠加超限。如需换仓，请先给出 close_short 决策", dec.Symbol)
			}
		}
	}

	// 构建交易上下文用于保证金检查
	ctx, err := at.buildTradingContext()
	if err != nil {
		return fmt.Errorf("构建交易上下文失败: %w", err)
	}

	// 开仓前再次验证保证金（防止在AI决策后保证金发生变化）
	if err := at.checkMarginAndBalanceSafety(ctx, dec); err != nil {
		return fmt.Errorf("保证金检查失败: %w", err)
	}

	// 双重检查：在开仓前再次检查持仓（防止竞态条件）
	positions, err = at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == dec.Symbol && pos["side"] == "short" {
				return fmt.Errorf("❌ 持仓检查失败：在开仓期间检测到已有持仓，可能是并发开仓导致的")
			}
		}
	}

	// 获取当前价格
	marketData, err := market.Get(dec.Symbol)
	if err != nil {
		return err
	}

	// 验证价格有效性（避免除零错误）
	if marketData.CurrentPrice <= 0 {
		return fmt.Errorf("当前价格无效或为0: %.4f", marketData.CurrentPrice)
	}

	// 计算数量（使用最新价格）
	quantity := dec.PositionSizeUSD / marketData.CurrentPrice
	
	// 立即格式化数量到正确精度（避免精度损失）
	formattedQuantityStr, err := at.trader.FormatQuantity(dec.Symbol, quantity)
	if err != nil {
		return fmt.Errorf("格式化数量失败: %w", err)
	}
	formattedQuantity, err := strconv.ParseFloat(formattedQuantityStr, 64)
	if err != nil {
		return fmt.Errorf("解析格式化后的数量失败: %w", err)
	}
	
	// 检查最小数量（使用格式化后的数量）
	minQuantity := MinPositionSizeUSD / marketData.CurrentPrice
	if formattedQuantity < minQuantity {
		return fmt.Errorf("计算出的数量过小(%.8f)，小于最小要求(%.8f)。可能因为仓位大小过小或价格过高", formattedQuantity, minQuantity)
	}

	actionRecord.Quantity = formattedQuantity
	actionRecord.Price = marketData.CurrentPrice

	// 开仓（使用格式化后的数量）
	order, err := at.trader.OpenShort(dec.Symbol, actionRecord.Quantity, dec.Leverage)
	if err != nil {
		return err
	}

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	log.Printf("  ✓ 开仓成功，订单ID: %v, 数量: %.4f", order["orderId"], actionRecord.Quantity)

	// 记录开仓时间
	posKey := dec.Symbol + "_short"
	firstSeenTime := time.Now().UnixMilli()
	at.positionTimeMu.Lock()
	at.positionFirstSeenTime[posKey] = firstSeenTime
	at.positionTimeMu.Unlock()
	// 保存到数据库
	if at.positionLogicManager != nil {
		if err := at.positionLogicManager.SaveFirstSeenTime(dec.Symbol, "short", firstSeenTime); err != nil {
			log.Printf("⚠️  保存持仓首次出现时间失败: %v", err)
		}
	}

	// 设置止损止盈并保存到PositionLogicManager（与逻辑一起持久化）
	if dec.StopLoss > 0 || dec.TakeProfit > 0 {
		// 先保存到PositionLogicManager（无论设置是否成功，都保存AI决策中的价格）
		if err := at.positionLogicManager.SaveStopLossAndTakeProfit(dec.Symbol, "short", dec.StopLoss, dec.TakeProfit); err != nil {
			log.Printf("  ⚠ 保存止损/止盈价格失败: %v", err)
		} else {
			log.Printf("  ✓ 已保存止损/止盈价格到逻辑管理器: 止损=%.4f, 止盈=%.4f", dec.StopLoss, dec.TakeProfit)
		}
		
		// 然后设置到交易所（如果失败不影响已保存的价格）
		if dec.StopLoss > 0 {
			if err := at.trader.SetStopLoss(dec.Symbol, "SHORT", quantity, dec.StopLoss); err != nil {
				log.Printf("  ⚠ 设置止损失败: %v (价格已保存到逻辑管理器)", err)
			} else {
				log.Printf("  ✓ 止损设置成功: %.4f", dec.StopLoss)
			}
		}
		if dec.TakeProfit > 0 {
			if err := at.trader.SetTakeProfit(dec.Symbol, "SHORT", quantity, dec.TakeProfit); err != nil {
				log.Printf("  ⚠ 设置止盈失败: %v (价格已保存到逻辑管理器)", err)
			} else {
				log.Printf("  ✓ 止盈设置成功: %.4f", dec.TakeProfit)
			}
		}
	}

	// 保存进场逻辑和出场逻辑（复用已获取的市场数据）
	if dec.Reasoning != "" {
		ctx := &decision.Context{
			MultiTimeframeConfig: at.config.MultiTimeframeConfig,
			MarketDataMap:        make(map[string]*market.Data),
		}
		// 复用前面已获取的市场数据，避免重复API调用
		ctx.MarketDataMap[dec.Symbol] = marketData
		
		// 保存进场逻辑
		entryLogic := decision.ExtractEntryLogicFromReasoning(dec.Reasoning, ctx, dec.Symbol)
		if err := at.positionLogicManager.SaveEntryLogic(dec.Symbol, "short", entryLogic); err != nil {
			log.Printf("  ⚠ 保存进场逻辑失败: %v", err)
		} else {
			log.Printf("  ✓ 已保存进场逻辑")
		}
		
		// 保存出场逻辑（如果提供）
		if dec.ExitReasoning != "" {
			exitLogic := decision.ExtractExitLogicFromReasoning(dec.ExitReasoning, ctx, dec.Symbol)
			if err := at.positionLogicManager.SaveExitLogic(dec.Symbol, "short", exitLogic); err != nil {
				log.Printf("  ⚠ 保存出场逻辑失败: %v", err)
			} else {
				log.Printf("  ✓ 已保存出场逻辑")
			}
		} else {
			log.Printf("  ⚠ 警告：开仓时未提供出场逻辑（exit_reasoning），建议在开仓时规划好出场逻辑")
		}
	}

	return nil
}

// executeCloseLongWithRecord 执行平多仓并记录详细信息（带并发保护）
func (at *AutoTrader) executeCloseLongWithRecord(dec *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平多仓: %s", dec.Symbol)
	
	posKey := dec.Symbol + "_long"
	
	// 先检查是否已被标记为强制平仓
	at.forcedCloseMu.RLock()
	_, alreadyForced := at.forcedClosedPositions[posKey]
	at.forcedCloseMu.RUnlock()
	if alreadyForced {
		return fmt.Errorf("持仓 %s long 已被强制平仓，跳过AI平仓操作", dec.Symbol)
	}
	
	// 获取该持仓的平仓锁（确保同一时间只有一个操作在平这个仓位）
	closingLock := at.getOrCreateClosingLock(posKey)
	closingLock.Lock()
	defer closingLock.Unlock()
	// 注意：只在成功时清理锁，失败时保留锁以便重试
	
	// 再次检查（双重检查）
	at.forcedCloseMu.RLock()
	_, alreadyForced = at.forcedClosedPositions[posKey]
	at.forcedCloseMu.RUnlock()
	if alreadyForced {
		return fmt.Errorf("持仓 %s long 已被强制平仓，跳过AI平仓操作", dec.Symbol)
	}


	// 获取当前价格
	marketData, err := market.Get(dec.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平仓
	order, err := at.trader.CloseLong(dec.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		// 平仓失败，保留锁以便重试
		return err
	}
	
	// 平仓成功后验证持仓是否真的被平掉（等待一小段时间让订单处理）
	time.Sleep(500 * time.Millisecond) // 等待500ms让交易所处理订单
	
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == dec.Symbol && pos["side"] == "long" {
				quantity := pos["positionAmt"].(float64)
				if quantity < 0 {
					quantity = -quantity
				}
				if quantity > 0.0001 { // 允许小的精度误差
					log.Printf("  ⚠️  警告：平仓后持仓仍存在，数量: %.8f", quantity)
					log.Printf("  ⚠️  订单可能正在处理中，如果5秒后持仓仍存在，请手动检查")
					// 记录到actionRecord以便后续监控
					actionRecord.Error = fmt.Sprintf("平仓后持仓仍存在: %.8f (可能正在处理中)", quantity)
					// 不返回错误，因为订单已提交，可能正在处理中
				}
			}
		}
	}
	
	// 平仓成功，清理锁
	at.cleanupClosingLock(posKey)

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// 清理持仓时间记录
	posKeyForTime := dec.Symbol + "_long"
	at.positionTimeMu.Lock()
	delete(at.positionFirstSeenTime, posKeyForTime)
	at.positionTimeMu.Unlock()

	// 记录交易历史（从持仓信息中获取开仓信息）
	// 保存出场逻辑（如果提供）
	if dec.Reasoning != "" {
		ctx := &decision.Context{
			MultiTimeframeConfig: at.config.MultiTimeframeConfig,
			MarketDataMap:        make(map[string]*market.Data),
		}
		if marketData, err := market.Get(dec.Symbol); err == nil {
			ctx.MarketDataMap[dec.Symbol] = marketData
			exitLogic := decision.ExtractExitLogicFromReasoning(dec.Reasoning, ctx, dec.Symbol)
			if err := at.positionLogicManager.SaveExitLogic(dec.Symbol, "long", exitLogic); err != nil {
				log.Printf("  ⚠ 保存出场逻辑失败: %v", err)
			} else {
				log.Printf("  ✓ 已保存出场逻辑")
			}
		}
	}

	// 删除持仓逻辑（平仓后不再需要，止损/止盈价格会一起删除）
	if err := at.positionLogicManager.DeleteLogic(dec.Symbol, "long"); err != nil {
		log.Printf("  ⚠ 删除持仓逻辑失败: %v", err)
	} else {
		log.Printf("  ✓ 已删除持仓逻辑（包含止损/止盈价格）")
	}

	at.recordTradeHistory("long", dec, actionRecord, false, "")

	log.Printf("  ✓ 平仓成功")
	return nil
}

// executeCloseShortWithRecord 执行平空仓并记录详细信息（带并发保护）
func (at *AutoTrader) executeCloseShortWithRecord(dec *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  🔄 平空仓: %s", dec.Symbol)
	
	posKey := dec.Symbol + "_short"
	
	// 先检查是否已被标记为强制平仓
	at.forcedCloseMu.RLock()
	_, alreadyForced := at.forcedClosedPositions[posKey]
	at.forcedCloseMu.RUnlock()
	if alreadyForced {
		return fmt.Errorf("持仓 %s short 已被强制平仓，跳过AI平仓操作", dec.Symbol)
	}
	
	// 获取该持仓的平仓锁（确保同一时间只有一个操作在平这个仓位）
	closingLock := at.getOrCreateClosingLock(posKey)
	closingLock.Lock()
	defer closingLock.Unlock()
	// 注意：只在成功时清理锁，失败时保留锁以便重试
	
	// 再次检查（双重检查）
	at.forcedCloseMu.RLock()
	_, alreadyForced = at.forcedClosedPositions[posKey]
	at.forcedCloseMu.RUnlock()
	if alreadyForced {
		return fmt.Errorf("持仓 %s short 已被强制平仓，跳过AI平仓操作", dec.Symbol)
	}


	// 获取当前价格
	marketData, err := market.Get(dec.Symbol)
	if err != nil {
		return err
	}
	actionRecord.Price = marketData.CurrentPrice

	// 平仓
	order, err := at.trader.CloseShort(dec.Symbol, 0) // 0 = 全部平仓
	if err != nil {
		// 平仓失败，保留锁以便重试
		return err
	}
	
	// 平仓成功后验证持仓是否真的被平掉（等待一小段时间让订单处理）
	time.Sleep(500 * time.Millisecond) // 等待500ms让交易所处理订单
	
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"] == dec.Symbol && pos["side"] == "short" {
				quantity := pos["positionAmt"].(float64)
				if quantity < 0 {
					quantity = -quantity
				}
				if quantity > 0.0001 { // 允许小的精度误差
					log.Printf("  ⚠️  警告：平仓后持仓仍存在，数量: %.8f", quantity)
					log.Printf("  ⚠️  订单可能正在处理中，如果5秒后持仓仍存在，请手动检查")
					// 记录到actionRecord以便后续监控
					actionRecord.Error = fmt.Sprintf("平仓后持仓仍存在: %.8f (可能正在处理中)", quantity)
					// 不返回错误，因为订单已提交，可能正在处理中
				}
			}
		}
	}
	
	// 平仓成功，清理锁
	at.cleanupClosingLock(posKey)

	// 记录订单ID
	if orderID, ok := order["orderId"].(int64); ok {
		actionRecord.OrderID = orderID
	}

	// 清理持仓时间记录和止损/止盈价格（通过PositionLogicManager删除逻辑时一起清理）
	posKeyForTime := dec.Symbol + "_short"
	at.positionTimeMu.Lock()
	delete(at.positionFirstSeenTime, posKeyForTime)
	at.positionTimeMu.Unlock()

	// 保存出场逻辑（如果提供，在删除逻辑之前保存）
	if dec.Reasoning != "" {
		ctx := &decision.Context{
			MultiTimeframeConfig: at.config.MultiTimeframeConfig,
			MarketDataMap:        make(map[string]*market.Data),
		}
		if marketData, err := market.Get(dec.Symbol); err == nil {
			ctx.MarketDataMap[dec.Symbol] = marketData
			exitLogic := decision.ExtractExitLogicFromReasoning(dec.Reasoning, ctx, dec.Symbol)
			if err := at.positionLogicManager.SaveExitLogic(dec.Symbol, "short", exitLogic); err != nil {
				log.Printf("  ⚠ 保存出场逻辑失败: %v", err)
			} else {
				log.Printf("  ✓ 已保存出场逻辑")
			}
		}
	}

	// 删除持仓逻辑（平仓后不再需要，止损/止盈价格会一起删除）
	if err := at.positionLogicManager.DeleteLogic(dec.Symbol, "short"); err != nil {
		log.Printf("  ⚠ 删除持仓逻辑失败: %v", err)
	} else {
		log.Printf("  ✓ 已删除持仓逻辑（包含止损/止盈价格）")
	}

	// 记录交易历史（从持仓信息中获取开仓信息）
	at.recordTradeHistory("short", dec, actionRecord, false, "")

	log.Printf("  ✓ 平仓成功")
	return nil
}

// findPositionBySymbol 根据symbol查找持仓（公共方法，消除代码重复）
func (at *AutoTrader) findPositionBySymbol(symbol string) (map[string]interface{}, string, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, "", fmt.Errorf("获取持仓失败: %w", err)
	}

	for _, pos := range positions {
		if pos["symbol"] == symbol {
			side := pos["side"].(string)
			quantity := pos["positionAmt"].(float64)
			if quantity < 0 {
				quantity = -quantity
			}
			if quantity > 0 {
				return pos, side, nil
			}
		}
	}

	return nil, "", fmt.Errorf("未找到 %s 的持仓", symbol)
}

// executeUpdateTakeProfit 更新止盈（用于调整现有持仓的止盈目标）
func (at *AutoTrader) executeUpdateTakeProfit(dec *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📋 开始更新止盈: %s -> %.4f", dec.Symbol, dec.TakeProfit)

	// 步骤1: 验证参数
	if dec.TakeProfit <= 0 {
		return fmt.Errorf("止盈价格必须大于0: %.4f", dec.TakeProfit)
	}

	// 步骤2: 查找持仓
	log.Printf("  🔍 查找 %s 的持仓...", dec.Symbol)
	foundPosition, positionSide, err := at.findPositionBySymbol(dec.Symbol)
	if err != nil {
		return fmt.Errorf("未找到 %s 的持仓，无法更新止盈: %w", dec.Symbol, err)
	}
	log.Printf("  ✓ 找到持仓: %s %s", dec.Symbol, positionSide)

	// 步骤3: 检查是否已经设置过相同或非常接近的止盈价格，防止频繁小幅调整
	existingLogic := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
	if existingLogic != nil && existingLogic.TakeProfit > 0 {
		// 计算价格差异百分比
		priceDiff := (dec.TakeProfit - existingLogic.TakeProfit) / existingLogic.TakeProfit
		if priceDiff < 0 {
			priceDiff = -priceDiff
		}
		// 如果价格差异小于0.5%，则认为变化太小，不值得更新，跳过执行
		// 这样可以避免频繁的小幅调整，减少不必要的订单操作
		if priceDiff < 0.005 {
			skipReason := fmt.Sprintf("新止盈价格 %.4f 与当前止盈 %.4f 差异太小（%.4f%%），小于0.5%阈值，跳过更新以避免频繁调整", 
				dec.TakeProfit, existingLogic.TakeProfit, priceDiff*100)
			log.Printf("  ⏭️  跳过更新止盈：%s %s", dec.Symbol, skipReason)
			actionRecord.Price = existingLogic.TakeProfit
			actionRecord.Quantity = foundPosition["positionAmt"].(float64)
			if actionRecord.Quantity < 0 {
				actionRecord.Quantity = -actionRecord.Quantity
			}
			actionRecord.Error = "SKIPPED: " + skipReason
			return nil
		}
	}

	// 步骤4: 获取持仓数量和当前价格
	quantity := foundPosition["positionAmt"].(float64)
	if quantity < 0 {
		quantity = -quantity
	}
	// 验证quantity的有效性
	if quantity <= 0 {
		return fmt.Errorf("持仓数量无效: %.4f", quantity)
	}
	// 验证quantity是否与实际的持仓数量匹配
	actualQuantity := foundPosition["positionAmt"].(float64)
	if math.Abs(quantity-math.Abs(actualQuantity)) > 0.0001 {
		log.Printf("  ⚠ 警告：持仓数量可能不匹配，计算值: %.4f, 实际值: %.4f，使用实际值", quantity, actualQuantity)
		quantity = math.Abs(actualQuantity)
	}

	// 获取当前价格
	log.Printf("  📊 获取 %s 的市场价格...", dec.Symbol)
	marketData, err := market.Get(dec.Symbol)
	if err != nil {
		return fmt.Errorf("获取 %s 的市场数据失败: %w", dec.Symbol, err)
	}
	if marketData == nil {
		return fmt.Errorf("获取到的 %s 市场数据为空", dec.Symbol)
	}
	if marketData.CurrentPrice <= 0 {
		return fmt.Errorf("获取到的 %s 当前价格无效: %.4f", dec.Symbol, marketData.CurrentPrice)
	}
	currentPrice := marketData.CurrentPrice
	actionRecord.Price = currentPrice
	actionRecord.Quantity = quantity
	log.Printf("  ✓ 当前价格: %.4f, 持仓数量: %.4f", currentPrice, quantity)

	// 步骤5: 验证止盈价格的合理性
	log.Printf("  ✅ 验证止盈价格合理性...")
	if positionSide == "long" {
		// 做多：止盈价应该大于当前价
		if dec.TakeProfit <= currentPrice {
			return fmt.Errorf("做多时止盈价(%.4f)必须大于当前价(%.4f)", dec.TakeProfit, currentPrice)
		}
	} else {
		// 做空：止盈价应该小于当前价
		if dec.TakeProfit >= currentPrice {
			return fmt.Errorf("做空时止盈价(%.4f)必须小于当前价(%.4f)", dec.TakeProfit, currentPrice)
		}
	}

	// 如果同时提供了止损，验证止损和止盈的相对位置
	if dec.StopLoss > 0 {
		if positionSide == "long" {
			// 做多：止损应该 < 当前价 < 止盈，且止损 < 止盈
			if dec.StopLoss >= dec.TakeProfit {
				return fmt.Errorf("做多时止损价(%.4f)必须小于止盈价(%.4f)", dec.StopLoss, dec.TakeProfit)
			}
			if dec.StopLoss >= currentPrice || dec.TakeProfit <= currentPrice {
				return fmt.Errorf("做多时当前价(%.4f)必须在止损(%.4f)和止盈(%.4f)之间", 
					currentPrice, dec.StopLoss, dec.TakeProfit)
			}
		} else {
			// 做空：止损应该 > 当前价 > 止盈，且止损 > 止盈
			if dec.StopLoss <= dec.TakeProfit {
				return fmt.Errorf("做空时止损价(%.4f)必须大于止盈价(%.4f)", dec.StopLoss, dec.TakeProfit)
			}
			if dec.TakeProfit >= currentPrice || dec.StopLoss <= currentPrice {
				return fmt.Errorf("做空时当前价(%.4f)必须在止盈(%.4f)和止损(%.4f)之间", 
					currentPrice, dec.TakeProfit, dec.StopLoss)
			}
		}
	}

	// 步骤6: 计算风险回报比（如果同时有止损和止盈，仅用于日志记录，不强制要求）
	// 注意：不再硬编码风险回报比检查，相信AI会根据提示词自行判断
	oldLogic := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
	takeProfit := dec.TakeProfit
	stopLoss := dec.StopLoss
	if stopLoss <= 0 && oldLogic != nil {
		stopLoss = oldLogic.StopLoss
	}
	if stopLoss > 0 && takeProfit > 0 {
		var riskRewardRatio float64
		if positionSide == "long" {
			risk := (currentPrice - stopLoss) / currentPrice
			reward := (takeProfit - currentPrice) / currentPrice
			if risk > 0 {
				riskRewardRatio = reward / risk
			}
		} else {
			risk := (stopLoss - currentPrice) / currentPrice
			reward := (currentPrice - takeProfit) / currentPrice
			if risk > 0 {
				riskRewardRatio = reward / risk
			}
		}
		// 仅记录风险回报比，不强制要求
		if riskRewardRatio > 0 {
			log.Printf("  ℹ️ 风险回报比: %.2f:1", riskRewardRatio)
		}
	}

	// 步骤7: 在取消订单前，先获取当前的止损值（如果Decision中没有提供，需要保留）
	preserveStopLoss := dec.StopLoss
	if preserveStopLoss <= 0 && oldLogic != nil && oldLogic.StopLoss > 0 {
		preserveStopLoss = oldLogic.StopLoss
		log.Printf("  ℹ️  检测到已有止损值 %.4f，将在更新止盈后保留", preserveStopLoss)
	}

	// 步骤8: 在取消订单前，先保存旧的订单信息（用于回滚）
	oldStopLossOrder := preserveStopLoss
	oldTakeProfitOrder := 0.0
	if oldLogic != nil && oldLogic.TakeProfit > 0 {
		oldTakeProfitOrder = oldLogic.TakeProfit
	}
	
	// 取消该币种的所有订单（删除旧的止损止盈单）
	log.Printf("  🗑️  取消旧的止损/止盈订单...")
	if err := at.trader.CancelAllOrders(dec.Symbol); err != nil {
		// 检查错误类型，如果是"没有订单"的错误，可以继续；否则应该返回错误
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "no orders") || 
		   strings.Contains(errStr, "not found") || 
		   strings.Contains(errStr, "没有订单") {
			log.Printf("  ℹ️  没有旧订单需要取消")
		} else {
			return fmt.Errorf("取消旧订单失败，无法继续更新: %w", err)
		}
	} else {
		log.Printf("  ✓ 已取消旧订单")
	}

	sideStr := "LONG"
	if positionSide == "short" {
		sideStr = "SHORT"
	}

	// 步骤9: 设置新的止盈单
	log.Printf("  ➕ 设置新的止盈订单: %.4f", dec.TakeProfit)
	if err := at.trader.SetTakeProfit(dec.Symbol, sideStr, quantity, dec.TakeProfit); err != nil {
		// 设置新订单失败，尝试恢复旧订单（回滚）
		log.Printf("  ⚠️  设置新止盈失败，尝试恢复旧订单...")
		rollbackErr := at.rollbackOrders(dec.Symbol, sideStr, quantity, oldStopLossOrder, oldTakeProfitOrder)
		if rollbackErr != nil {
			log.Printf("  ❌ 回滚失败: %v，旧订单已丢失，需要手动检查", rollbackErr)
			return fmt.Errorf("设置新止盈失败且回滚失败: %w (回滚错误: %v)", err, rollbackErr)
		}
		log.Printf("  ✓ 已恢复旧订单")
		return fmt.Errorf("设置新止盈失败，已恢复旧订单: %w", err)
	}
	log.Printf("  ✓ 止盈订单设置成功")

	// 步骤10: 如果Decision中提供了StopLoss，或者需要保留已有的止损，重新设置止损（保持止损止盈同步）
	if preserveStopLoss > 0 {
		log.Printf("  ➕ 同步设置止损: %.4f", preserveStopLoss)
		if err := at.trader.SetStopLoss(dec.Symbol, sideStr, quantity, preserveStopLoss); err != nil {
			// 设置止损失败，尝试恢复旧订单（回滚）
			log.Printf("  ⚠️  同步设置止损失败，尝试恢复旧订单...")
			rollbackErr := at.rollbackOrders(dec.Symbol, sideStr, quantity, oldStopLossOrder, oldTakeProfitOrder)
			if rollbackErr != nil {
				log.Printf("  ❌ 回滚失败: %v，旧订单已丢失，需要手动检查", rollbackErr)
				return fmt.Errorf("同步设置止损失败且回滚失败: %w (回滚错误: %v)", err, rollbackErr)
			}
			log.Printf("  ✓ 已恢复旧订单")
			return fmt.Errorf("同步设置止损失败，已恢复旧订单: %w", err)
		}
		log.Printf("  ✓ 止损已同步: %.4f", preserveStopLoss)
	}

	// 步骤11: 保存止盈价格到PositionLogicManager（如果保留了止损，也要保存）
	saveStopLoss := dec.StopLoss
	if saveStopLoss <= 0 && preserveStopLoss > 0 {
		saveStopLoss = preserveStopLoss
	}
	
	if saveStopLoss > 0 {
		log.Printf("  ✓ 止盈已更新: %s %s 止盈 %.4f，止损 %.4f", dec.Symbol, positionSide, dec.TakeProfit, saveStopLoss)
	} else {
		log.Printf("  ✓ 止盈已更新: %s %s 止盈 %.4f（注意：止损订单已被取消，建议使用update_sl重新设置止损）", dec.Symbol, positionSide, dec.TakeProfit)
	}
	
	// 在保存前，先获取当前值以确认保存逻辑正确
	oldLogicBeforeSave := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
	if oldLogicBeforeSave != nil {
		log.Printf("  🔍 保存前当前值: 止损=%.4f, 止盈=%.4f", oldLogicBeforeSave.StopLoss, oldLogicBeforeSave.TakeProfit)
	}
	
	if err := at.positionLogicManager.SaveStopLossAndTakeProfit(dec.Symbol, positionSide, saveStopLoss, dec.TakeProfit); err != nil {
		log.Printf("  ⚠ 保存止损/止盈价格失败: %v", err)
	} else {
		// 保存后立即验证读取，确认保存成功
		verifyLogic := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
		if verifyLogic != nil {
			if saveStopLoss > 0 {
				log.Printf("  ✓ 已保存止损/止盈价格到逻辑管理器: 止损=%.4f, 止盈=%.4f (验证: 止损=%.4f, 止盈=%.4f)", 
					saveStopLoss, dec.TakeProfit, verifyLogic.StopLoss, verifyLogic.TakeProfit)
			} else {
				oldStopLoss := 0.0
				if oldLogicBeforeSave != nil {
					oldStopLoss = oldLogicBeforeSave.StopLoss
				}
				log.Printf("  ✓ 已保存止盈价格到逻辑管理器: 止盈=%.4f (止损保持不变为%.4f) (验证: 止损=%.4f, 止盈=%.4f)", 
					dec.TakeProfit, oldStopLoss, verifyLogic.StopLoss, verifyLogic.TakeProfit)
			}
		} else {
			log.Printf("  ⚠ 保存后验证读取失败: 无法读取到保存的值")
		}
	}
	
	return nil
}

// executeUpdateStopLoss 更新止损（用于调整现有持仓的止损位置）
func (at *AutoTrader) executeUpdateStopLoss(dec *decision.Decision, actionRecord *logger.DecisionAction) error {
	log.Printf("  📋 开始更新止损: %s -> %.4f", dec.Symbol, dec.StopLoss)

	// 步骤1: 验证参数
	if dec.StopLoss <= 0 {
		return fmt.Errorf("止损价格必须大于0: %.4f", dec.StopLoss)
	}

	// 步骤2: 查找持仓
	log.Printf("  🔍 查找 %s 的持仓...", dec.Symbol)
	foundPosition, positionSide, err := at.findPositionBySymbol(dec.Symbol)
	if err != nil {
		return fmt.Errorf("未找到 %s 的持仓，无法更新止损: %w", dec.Symbol, err)
	}
	log.Printf("  ✓ 找到持仓: %s %s", dec.Symbol, positionSide)

	// 步骤3: 检查是否已经设置过相同或非常接近的止损价格，防止频繁小幅调整
	existingLogic := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
	if existingLogic != nil && existingLogic.StopLoss > 0 {
		// 计算价格差异百分比
		priceDiff := (dec.StopLoss - existingLogic.StopLoss) / existingLogic.StopLoss
		if priceDiff < 0 {
			priceDiff = -priceDiff
		}
		// 如果价格差异小于0.5%，则认为变化太小，不值得更新，跳过执行
		// 这样可以避免频繁的小幅调整，减少不必要的订单操作
		if priceDiff < 0.005 {
			skipReason := fmt.Sprintf("新止损价格 %.4f 与当前止损 %.4f 差异太小（%.4f%%），小于0.5%阈值，跳过更新以避免频繁调整", 
				dec.StopLoss, existingLogic.StopLoss, priceDiff*100)
			log.Printf("  ⏭️  跳过更新止损：%s %s", dec.Symbol, skipReason)
			actionRecord.Price = existingLogic.StopLoss
			actionRecord.Quantity = foundPosition["positionAmt"].(float64)
			if actionRecord.Quantity < 0 {
				actionRecord.Quantity = -actionRecord.Quantity
			}
			actionRecord.Error = "SKIPPED: " + skipReason
			return nil
		}
	}

	// 步骤4: 获取持仓数量和当前价格
	quantity := foundPosition["positionAmt"].(float64)
	if quantity < 0 {
		quantity = -quantity
	}
	// 验证quantity的有效性
	if quantity <= 0 {
		return fmt.Errorf("持仓数量无效: %.4f", quantity)
	}
	// 验证quantity是否与实际的持仓数量匹配
	actualQuantity := foundPosition["positionAmt"].(float64)
	if math.Abs(quantity-math.Abs(actualQuantity)) > 0.0001 {
		log.Printf("  ⚠ 警告：持仓数量可能不匹配，计算值: %.4f, 实际值: %.4f，使用实际值", quantity, actualQuantity)
		quantity = math.Abs(actualQuantity)
	}

	// 获取当前价格
	log.Printf("  📊 获取 %s 的市场价格...", dec.Symbol)
	marketData, err := market.Get(dec.Symbol)
	if err != nil {
		return fmt.Errorf("获取 %s 的市场数据失败: %w", dec.Symbol, err)
	}
	if marketData == nil {
		return fmt.Errorf("获取到的 %s 市场数据为空", dec.Symbol)
	}
	if marketData.CurrentPrice <= 0 {
		return fmt.Errorf("获取到的 %s 当前价格无效: %.4f", dec.Symbol, marketData.CurrentPrice)
	}
	currentPrice := marketData.CurrentPrice
	actionRecord.Price = currentPrice
	actionRecord.Quantity = quantity
	log.Printf("  ✓ 当前价格: %.4f, 持仓数量: %.4f", currentPrice, quantity)

	// 步骤5: 验证止损价格的合理性
	log.Printf("  ✅ 验证止损价格合理性...")
	if positionSide == "long" {
		// 做多：止损价应该小于当前价
		if dec.StopLoss >= currentPrice {
			return fmt.Errorf("做多时止损价(%.4f)必须小于当前价(%.4f)", dec.StopLoss, currentPrice)
		}
	} else {
		// 做空：止损价应该大于当前价
		if dec.StopLoss <= currentPrice {
			return fmt.Errorf("做空时止损价(%.4f)必须大于当前价(%.4f)", dec.StopLoss, currentPrice)
		}
	}

	// 验证移动止损的合理性（只能向更有利的方向移动）
	oldLogic := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
	if oldLogic != nil && oldLogic.StopLoss > 0 {
		if positionSide == "long" {
			// 做多：新止损应该 >= 旧止损（只能向上移动，不能向下）
			if dec.StopLoss < oldLogic.StopLoss {
				return fmt.Errorf("做多时移动止损只能向上移动，新止损(%.4f)不能低于旧止损(%.4f)", 
					dec.StopLoss, oldLogic.StopLoss)
			}
		} else {
			// 做空：新止损应该 <= 旧止损（只能向下移动，不能向上）
			if dec.StopLoss > oldLogic.StopLoss {
				return fmt.Errorf("做空时移动止损只能向下移动，新止损(%.4f)不能高于旧止损(%.4f)", 
					dec.StopLoss, oldLogic.StopLoss)
			}
		}
	}

	// 如果同时提供了止盈，验证止损和止盈的相对位置
	if dec.TakeProfit > 0 {
		if positionSide == "long" {
			// 做多：止损应该 < 当前价 < 止盈，且止损 < 止盈
			if dec.StopLoss >= dec.TakeProfit {
				return fmt.Errorf("做多时止损价(%.4f)必须小于止盈价(%.4f)", dec.StopLoss, dec.TakeProfit)
			}
			if dec.StopLoss >= currentPrice || dec.TakeProfit <= currentPrice {
				return fmt.Errorf("做多时当前价(%.4f)必须在止损(%.4f)和止盈(%.4f)之间", 
					currentPrice, dec.StopLoss, dec.TakeProfit)
			}
		} else {
			// 做空：止损应该 > 当前价 > 止盈，且止损 > 止盈
			if dec.StopLoss <= dec.TakeProfit {
				return fmt.Errorf("做空时止损价(%.4f)必须大于止盈价(%.4f)", dec.StopLoss, dec.TakeProfit)
			}
			if dec.TakeProfit >= currentPrice || dec.StopLoss <= currentPrice {
				return fmt.Errorf("做空时当前价(%.4f)必须在止盈(%.4f)和止损(%.4f)之间", 
					currentPrice, dec.TakeProfit, dec.StopLoss)
			}
		}
	}

	// 步骤6: 计算风险回报比（如果同时有止损和止盈，仅用于日志记录，不强制要求）
	// 注意：不再硬编码风险回报比检查，相信AI会根据提示词自行判断
	takeProfit := dec.TakeProfit
	if takeProfit <= 0 && oldLogic != nil {
		takeProfit = oldLogic.TakeProfit
	}
	if takeProfit > 0 {
		var riskRewardRatio float64
		if positionSide == "long" {
			risk := (currentPrice - dec.StopLoss) / currentPrice
			reward := (takeProfit - currentPrice) / currentPrice
			if risk > 0 {
				riskRewardRatio = reward / risk
			}
		} else {
			risk := (dec.StopLoss - currentPrice) / currentPrice
			reward := (currentPrice - takeProfit) / currentPrice
			if risk > 0 {
				riskRewardRatio = reward / risk
			}
		}
		// 仅记录风险回报比，不强制要求
		if riskRewardRatio > 0 {
			log.Printf("  ℹ️ 风险回报比: %.2f:1", riskRewardRatio)
		}
	}

	// 步骤7: 在取消订单前，先获取当前的止盈值（如果Decision中没有提供，需要保留）
	preserveTakeProfit := dec.TakeProfit
	if preserveTakeProfit <= 0 && oldLogic != nil && oldLogic.TakeProfit > 0 {
		preserveTakeProfit = oldLogic.TakeProfit
		log.Printf("  ℹ️  检测到已有止盈值 %.4f，将在更新止损后保留", preserveTakeProfit)
	}

	// 步骤8: 在取消订单前，先保存旧的订单信息（用于回滚）
	oldStopLossOrder := 0.0
	if oldLogic != nil && oldLogic.StopLoss > 0 {
		oldStopLossOrder = oldLogic.StopLoss
	}
	oldTakeProfitOrder := preserveTakeProfit
	
	// 取消该币种的所有订单（删除旧的止损止盈单）
	log.Printf("  🗑️  取消旧的止损/止盈订单...")
	if err := at.trader.CancelAllOrders(dec.Symbol); err != nil {
		// 检查错误类型，如果是"没有订单"的错误，可以继续；否则应该返回错误
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "no orders") || 
		   strings.Contains(errStr, "not found") || 
		   strings.Contains(errStr, "没有订单") {
			log.Printf("  ℹ️  没有旧订单需要取消")
		} else {
			return fmt.Errorf("取消旧订单失败，无法继续更新: %w", err)
		}
	} else {
		log.Printf("  ✓ 已取消旧订单")
	}

	sideStr := "LONG"
	if positionSide == "short" {
		sideStr = "SHORT"
	}

	// 步骤9: 设置新的止损单
	log.Printf("  ➕ 设置新的止损订单: %.4f", dec.StopLoss)
	if err := at.trader.SetStopLoss(dec.Symbol, sideStr, quantity, dec.StopLoss); err != nil {
		// 设置新订单失败，尝试恢复旧订单（回滚）
		log.Printf("  ⚠️  设置新止损失败，尝试恢复旧订单...")
		rollbackErr := at.rollbackOrders(dec.Symbol, sideStr, quantity, oldStopLossOrder, oldTakeProfitOrder)
		if rollbackErr != nil {
			log.Printf("  ❌ 回滚失败: %v，旧订单已丢失，需要手动检查", rollbackErr)
			return fmt.Errorf("设置新止损失败且回滚失败: %w (回滚错误: %v)", err, rollbackErr)
		}
		log.Printf("  ✓ 已恢复旧订单")
		return fmt.Errorf("设置新止损失败，已恢复旧订单: %w", err)
	}
	log.Printf("  ✓ 止损订单设置成功")

	// 步骤10: 如果Decision中提供了TakeProfit，或者需要保留已有的止盈，重新设置止盈（保持止损止盈同步）
	if preserveTakeProfit > 0 {
		log.Printf("  ➕ 同步设置止盈: %.4f", preserveTakeProfit)
		if err := at.trader.SetTakeProfit(dec.Symbol, sideStr, quantity, preserveTakeProfit); err != nil {
			// 设置止盈失败，尝试恢复旧订单（回滚）
			log.Printf("  ⚠️  同步设置止盈失败，尝试恢复旧订单...")
			rollbackErr := at.rollbackOrders(dec.Symbol, sideStr, quantity, oldStopLossOrder, oldTakeProfitOrder)
			if rollbackErr != nil {
				log.Printf("  ❌ 回滚失败: %v，旧订单已丢失，需要手动检查", rollbackErr)
				return fmt.Errorf("同步设置止盈失败且回滚失败: %w (回滚错误: %v)", err, rollbackErr)
			}
			log.Printf("  ✓ 已恢复旧订单")
			return fmt.Errorf("同步设置止盈失败，已恢复旧订单: %w", err)
		}
		log.Printf("  ✓ 止盈已同步: %.4f", preserveTakeProfit)
	}

	// 步骤11: 保存止损价格到PositionLogicManager（如果保留了止盈，也要保存）
	saveTakeProfit := dec.TakeProfit
	if saveTakeProfit <= 0 && preserveTakeProfit > 0 {
		saveTakeProfit = preserveTakeProfit
	}
	
	if saveTakeProfit > 0 {
		log.Printf("  ✓ 止损已更新: %s %s 止损 %.4f，止盈 %.4f", dec.Symbol, positionSide, dec.StopLoss, saveTakeProfit)
	} else {
		log.Printf("  ✓ 止损已更新: %s %s 止损 %.4f（注意：止盈订单已被取消，建议使用update_tp重新设置止盈）", dec.Symbol, positionSide, dec.StopLoss)
	}
	
	// 在保存前，先获取当前值以确认保存逻辑正确
	oldLogicBeforeSave := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
	if oldLogicBeforeSave != nil {
		log.Printf("  🔍 保存前当前值: 止损=%.4f, 止盈=%.4f", oldLogicBeforeSave.StopLoss, oldLogicBeforeSave.TakeProfit)
	}
	
	if err := at.positionLogicManager.SaveStopLossAndTakeProfit(dec.Symbol, positionSide, dec.StopLoss, saveTakeProfit); err != nil {
		log.Printf("  ⚠ 保存止损/止盈价格失败: %v", err)
	} else {
		// 保存后立即验证读取，确认保存成功
		verifyLogic := at.positionLogicManager.GetLogic(dec.Symbol, positionSide)
		if verifyLogic != nil {
			if dec.TakeProfit > 0 {
				log.Printf("  ✓ 已保存止损/止盈价格到逻辑管理器: 止损=%.4f, 止盈=%.4f (验证: 止损=%.4f, 止盈=%.4f)", 
					dec.StopLoss, dec.TakeProfit, verifyLogic.StopLoss, verifyLogic.TakeProfit)
			} else {
				oldTakeProfit := 0.0
				if oldLogicBeforeSave != nil {
					oldTakeProfit = oldLogicBeforeSave.TakeProfit
				}
				log.Printf("  ✓ 已保存止损价格到逻辑管理器: 止损=%.4f (止盈保持不变为%.4f) (验证: 止损=%.4f, 止盈=%.4f)", 
					dec.StopLoss, oldTakeProfit, verifyLogic.StopLoss, verifyLogic.TakeProfit)
			}
		} else {
			log.Printf("  ⚠ 保存后验证读取失败: 无法读取到保存的值")
		}
	}
	
	return nil
}

// recordTradeHistory 记录交易历史（从决策记录中查找开仓信息）
func (at *AutoTrader) recordTradeHistory(side string, decision *decision.Decision, closeAction *logger.DecisionAction, isForced bool, forcedReason string) {
	if at.storageAdapter == nil {
		return
	}

	decisionStorage := at.storageAdapter.GetDecisionStorage()
	if decisionStorage == nil {
		return
	}

	// 从数据库获取最近的决策记录，查找对应的开仓操作
	records, err := decisionStorage.GetLatestRecords(at.id, 1000)
	if err != nil {
		log.Printf("⚠️  查找开仓记录失败: %v", err)
		// 如果找不到，尝试从持仓信息中获取
		at.recordTradeHistoryFromPosition(side, decision.Symbol, closeAction, isForced, forcedReason)
		return
	}

	// 查找匹配的开仓记录（必须是在closeAction之前、且未被平仓的开仓）
	var openAction *logger.DecisionAction
	var openCycleNum int
	closeTime := closeAction.Timestamp

	// 从新到旧遍历记录
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		
		// 解析decisions字段
		var decisions []logger.DecisionAction
		if err := json.Unmarshal(record.Decisions, &decisions); err != nil {
			continue
		}

		for _, action := range decisions {
			if !action.Success {
				continue
			}

			// 检查是否是对应持仓的开仓操作
			var actionSide string
			if action.Action == "open_long" || action.Action == "close_long" {
				actionSide = "long"
			} else if action.Action == "open_short" || action.Action == "close_short" {
				actionSide = "short"
			}

			if action.Symbol == decision.Symbol && actionSide == side {
				if action.Action == "open_long" || action.Action == "open_short" {
					// 检查这个开仓是否在closeAction之前
					if action.Timestamp.After(closeTime) {
						continue
					}
					
					// 检查这个开仓之后是否已经被平仓（在closeAction之前）
					hasBeenClosed := false
					// 从当前记录到closeAction所在的记录之间查找平仓操作
					for j := i; j < len(records); j++ {
						var laterDecisions []logger.DecisionAction
						if err := json.Unmarshal(records[j].Decisions, &laterDecisions); err != nil {
							continue
						}
						for _, laterAction := range laterDecisions {
							if !laterAction.Success {
								continue
							}
							if laterAction.Symbol == decision.Symbol {
								if (side == "long" && laterAction.Action == "close_long") ||
									(side == "short" && laterAction.Action == "close_short") {
									// 如果找到了平仓记录，且时间在closeAction之前，说明这个开仓已经被平仓
									if laterAction.Timestamp.Before(closeTime) && !laterAction.Timestamp.Equal(closeTime) {
										hasBeenClosed = true
										break
									}
								}
							}
						}
						if hasBeenClosed {
							break
						}
					}

					// 如果这个开仓没有被平仓，或者被closeAction平仓，则匹配
					if !hasBeenClosed {
						openAction = &action
						openCycleNum = record.CycleNumber
						break
					}
				}
			}
		}
		if openAction != nil {
			break
		}
	}

	if openAction == nil {
		// 如果找不到开仓记录，尝试从持仓信息中获取（可能是在系统外开仓的）
		at.recordTradeHistoryFromPosition(side, decision.Symbol, closeAction, isForced, forcedReason)
		return
	}

	// 构建交易记录
	trade := at.buildTradeRecord(decision.Symbol, side, openAction, closeAction, openCycleNum, atomic.LoadInt64(&at.callCount), isForced, forcedReason, decision.Reasoning, decision.Reasoning)
	
	// 保存交易历史到数据库
	if at.storageAdapter != nil {
		tradeStorage := at.storageAdapter.GetTradeStorage()
		if tradeStorage != nil {
			// 转换logger.TradeRecord到storage.TradeRecord
			dbTrade := &storage.TradeRecord{
				TradeID:        trade.TradeID,
				Symbol:         trade.Symbol,
				Side:           trade.Side,
				OpenTime:       trade.OpenTime,
				OpenPrice:      trade.OpenPrice,
				OpenQuantity:   trade.OpenQuantity,
				OpenLeverage:   trade.OpenLeverage,
				OpenOrderID:    trade.OpenOrderID,
				OpenReason:     trade.OpenReason,
				OpenCycleNum:   trade.OpenCycleNum,
				CloseTime:      trade.CloseTime,
				ClosePrice:     trade.ClosePrice,
				CloseQuantity:  trade.CloseQuantity,
				CloseOrderID:   trade.CloseOrderID,
				CloseReason:    trade.CloseReason,
				CloseCycleNum:  trade.CloseCycleNum,
				IsForced:       trade.IsForced,
				ForcedReason:   trade.ForcedReason,
				Duration:       trade.Duration,
				PositionValue:  trade.PositionValue,
				MarginUsed:     trade.MarginUsed,
				PnL:            trade.PnL,
				PnLPct:         trade.PnLPct,
				WasStopLoss:    trade.WasStopLoss,
				Success:        trade.Success,
				Error:          trade.Error,
			}

			if err := tradeStorage.LogTrade(dbTrade); err != nil {
				log.Printf("⚠️  保存交易历史到数据库失败: %v", err)
			}
		}
	}
}

// recordTradeHistoryFromAction 记录交易历史（从强制平仓操作构建，不依赖决策记录）
func (at *AutoTrader) recordTradeHistoryFromAction(symbol, side string, closeAction *logger.DecisionAction, isForced bool, forcedReason string) {
	// 尝试从持仓信息中获取开仓信息（平仓前应该还有持仓信息）
	at.recordTradeHistoryFromPosition(side, symbol, closeAction, isForced, forcedReason)
}

// recordTradeHistoryFromPosition 从持仓信息中记录交易历史（用于找不到开仓记录的情况）
func (at *AutoTrader) recordTradeHistoryFromPosition(side, symbol string, closeAction *logger.DecisionAction, isForced bool, forcedReason string) {
	// 尝试从positionFirstSeenTime获取开仓时间
	posKey := symbol + "_" + side
	at.positionTimeMu.RLock()
	var openTime time.Time
	var hasOpenTime bool
	if ts, exists := at.positionFirstSeenTime[posKey]; exists {
		openTime = time.Unix(ts/1000, (ts%1000)*1000000)
		hasOpenTime = true
	}
	at.positionTimeMu.RUnlock()

	// 获取当前持仓信息（平仓后可能已经不存在，尝试从决策记录中获取）
	var entryPrice, quantity, leverage float64
	positions, err := at.trader.GetPositions()
	if err == nil {
		for _, pos := range positions {
			if pos["symbol"].(string) == symbol && pos["side"].(string) == side {
				entryPrice = pos["entryPrice"].(float64)
				qty := pos["positionAmt"].(float64)
				if qty < 0 {
					qty = -qty
				}
				quantity = qty
				if lev, ok := pos["leverage"].(float64); ok {
					leverage = lev
				}
				break
			}
		}
	}

	// 如果仍然无法获取开仓价格，尝试从positionLogicManager获取
	if entryPrice == 0 && at.positionLogicManager != nil {
		// 从持仓逻辑管理器获取更完整的持仓信息
		logic := at.positionLogicManager.GetLogic(symbol, side)
		if logic != nil {
			// 如果持仓逻辑中包含多时间框架逻辑，可能有入场价格信息
			if logic.EntryLogic != nil {
				// 这里我们需要检查是否有办法从entry logic的上下文中获取入场价格
				log.Printf("ℹ️  从持仓逻辑管理器找到了 %s %s 的入场逻辑，但可能没有直接的价格信息", symbol, side)
			}
		}
	}

	// 尝试从决策存储中获取最近的开仓决策（无论是否已有entryPrice，都需要查找开仓时间）
	if at.storageAdapter != nil {
		decisionStorage := at.storageAdapter.GetDecisionStorage()
		if decisionStorage != nil {
			// 如果还没有开仓时间，尝试从决策记录中查找
			if !hasOpenTime {
				// 获取最近的决策记录 - 使用正确的函数名GetLatestRecords
				records, err := decisionStorage.GetLatestRecords(at.id, 100) // 增加查找数量
				if err == nil {
					// 从最新的记录开始向前查找，直到找到对应符号和方向的开仓决策
					for i := len(records) - 1; i >= 0; i-- {
						var decisionsList []decision.Decision
						if err := json.Unmarshal(records[i].Decisions, &decisionsList); err == nil {
							for _, d := range decisionsList {
								// 查找匹配的开仓决策
								isOpenLong := d.Action == "open_long" && d.Symbol == symbol && side == "long"
								isOpenShort := d.Action == "open_short" && d.Symbol == symbol && side == "short"
								
								if isOpenLong || isOpenShort {
									// 找到开仓决策，使用记录的时间戳作为开仓时间
									openTime = records[i].Timestamp
									hasOpenTime = true
									log.Printf("ℹ️  从决策历史找到 %s %s 的开仓时间: %s", symbol, side, openTime.Format("2006-01-02 15:04:05"))
									break
								}
							}
							if hasOpenTime {
								break
							}
						}
					}
				}
			}
			
			// 如果还没有找到开仓价格，继续查找
			if entryPrice == 0 {
				records, err := decisionStorage.GetLatestRecords(at.id, 100)
				if err == nil {
					for i := len(records) - 1; i >= 0; i-- {
						var decisionsList []decision.Decision
						if err := json.Unmarshal(records[i].Decisions, &decisionsList); err == nil {
							for _, d := range decisionsList {
								isOpenLong := d.Action == "open_long" && d.Symbol == symbol && side == "long"
								isOpenShort := d.Action == "open_short" && d.Symbol == symbol && side == "short"
								
								if isOpenLong || isOpenShort {
									// 这是一个匹配的开仓决策，记录开仓价格和数量
									entryPrice = closeAction.Price // 使用closeAction中的价格作为初始估算（强制平仓时这可能是接近的价格）
									
									// 决策结构中没有EntryPrice字段，但我们有PositionSizeUSD
									// 我们无法直接获得入场价格，但可以尝试其他方法
									if d.PositionSizeUSD > 0 {
										log.Printf("⚠️  找到开仓决策但无法获取入场价格，使用估算值")
									} else {
										log.Printf("⚠️  找到开仓决策但缺少完整信息，使用估算值")
										entryPrice = closeAction.Price
										quantity = closeAction.Quantity
										leverage = float64(closeAction.Leverage)
									}
									
									// 如果还没有开仓时间，使用这个记录的时间戳
									if !hasOpenTime {
										openTime = records[i].Timestamp
										hasOpenTime = true
									}
									break
								}
							}
							if entryPrice != 0 {
								break
							}
						}
					}
				}
			}
		}
	}

	// 如果仍然无法获取开仓价格，尝试从position_logic数据库获取
	if entryPrice == 0 && at.storageAdapter != nil {
		logicStorage := at.storageAdapter.GetPositionLogicStorage()
		if logicStorage != nil {
			// 使用PositionLogicStorage的GetLogic方法（返回两个值）
			logic, err := logicStorage.GetLogic(symbol, side)
			if err == nil && logic != nil {
				// 这里我们没有直接的价格信息，但是可能可以推断出一些信息
				log.Printf("ℹ️  从position_logic数据库获取到 %s %s 的逻辑记录，但没有直接的价格信息", symbol, side)
			}
		}
	}

	// 如果还是无法获取开仓价格，跳过记录
	if entryPrice == 0 {
		log.Printf("❌ 无法获取 %s %s 的开仓价格，跳过交易历史记录", symbol, side)
		return
	}
	
	// 如果还是无法获取开仓时间，使用平仓时间减去一个合理的默认值（比如当前持仓的平均时长）
	// 但为了避免显示错误的duration，我们使用一个更保守的估算：平仓时间减去1小时
	if !hasOpenTime {
		log.Printf("⚠️  无法获取 %s %s 的开仓时间，使用平仓时间减去1小时作为估算", symbol, side)
		openTime = closeAction.Timestamp.Add(-1 * time.Hour)
	}

	// 验证获取到的数据是否合理
	if quantity == 0 {
		// 如果数量为0，尝试通过其他方式估算
		if closeAction.Quantity != 0 {
			quantity = closeAction.Quantity
		} else {
			// 使用一个默认值或从closeAction中推断
			log.Printf("⚠️  数量为0，使用默认估算值")
			quantity = 1.0 // 设置一个默认数量，这可能不准确
		}
	}
	
	if leverage == 0 {
		// 如果杠杆为0，从closeAction中获取或使用默认值
		if closeAction.Leverage != 0 {
			leverage = float64(closeAction.Leverage)
		} else {
			leverage = 10.0 // 默认杠杆
		}
	}

	// 构建临时的开仓操作记录
	openAction := &logger.DecisionAction{
		Symbol:    symbol,
		Action:    fmt.Sprintf("open_%s", side),
		Price:     entryPrice,
		Quantity:  quantity,
		Leverage:  int(leverage),
		Timestamp: openTime,
		Success:   true,
	}

	// 构建交易记录
	trade := at.buildTradeRecord(symbol, side, openAction, closeAction, 0, atomic.LoadInt64(&at.callCount), isForced, forcedReason, "系统外开仓", "")
	
	// 保存交易历史到数据库
	if at.storageAdapter != nil {
		tradeStorage := at.storageAdapter.GetTradeStorage()
		if tradeStorage != nil {
			// 转换logger.TradeRecord到storage.TradeRecord
			dbTrade := &storage.TradeRecord{
				TradeID:        trade.TradeID,
				Symbol:         trade.Symbol,
				Side:           trade.Side,
				OpenTime:       trade.OpenTime,
				OpenPrice:      trade.OpenPrice,
				OpenQuantity:   trade.OpenQuantity,
				OpenLeverage:   trade.OpenLeverage,
				OpenOrderID:    trade.OpenOrderID,
				OpenReason:     trade.OpenReason,
				OpenCycleNum:   trade.OpenCycleNum,
				CloseTime:      trade.CloseTime,
				ClosePrice:     trade.ClosePrice,
				CloseQuantity:  trade.CloseQuantity,
				CloseOrderID:   trade.CloseOrderID,
				CloseReason:    trade.CloseReason,
				CloseCycleNum:  trade.CloseCycleNum,
				IsForced:       trade.IsForced,
				ForcedReason:   trade.ForcedReason,
				Duration:       trade.Duration,
				PositionValue:  trade.PositionValue,
				MarginUsed:     trade.MarginUsed,
				PnL:            trade.PnL,
				PnLPct:         trade.PnLPct,
				WasStopLoss:    trade.WasStopLoss,
				Success:        trade.Success,
				Error:          trade.Error,
			}

			if err := tradeStorage.LogTrade(dbTrade); err != nil {
				log.Printf("⚠️  保存交易历史到数据库失败: %v", err)
			} else {
				log.Printf("✅ 强制平仓交易历史已记录: %s %s, 盈亏: %.2f USDT (%.2f%%)", symbol, side, trade.PnL, trade.PnLPct)
			}
		}
	}
}

// buildTradeRecord 构建完整的交易记录
func (at *AutoTrader) buildTradeRecord(symbol, side string, openAction, closeAction *logger.DecisionAction, openCycleNum int, closeCycleNum int64, isForced bool, forcedReason, openReason, closeReason string) *logger.TradeRecord {
	// 计算盈亏
	var pnl float64
	if side == "long" {
		pnl = openAction.Quantity * (closeAction.Price - openAction.Price)
	} else {
		pnl = openAction.Quantity * (openAction.Price - closeAction.Price)
	}

	// 计算持仓价值和保证金
	positionValue := openAction.Quantity * openAction.Price
	marginUsed := positionValue / float64(openAction.Leverage)
	pnlPct := 0.0
	if marginUsed > 0 {
		pnlPct = (pnl / marginUsed) * 100
	}

	// 计算持仓时长
	duration := closeAction.Timestamp.Sub(openAction.Timestamp)

	// 生成交易ID
	tradeID := fmt.Sprintf("%s_%s_%d", symbol, side, openAction.Timestamp.Unix())

	return &logger.TradeRecord{
		TradeID:       tradeID,
		Symbol:        symbol,
		Side:          side,
		OpenTime:      openAction.Timestamp,
		OpenPrice:     openAction.Price,
		OpenQuantity:  openAction.Quantity,
		OpenLeverage:  openAction.Leverage,
		OpenOrderID:   openAction.OrderID,
		OpenReason:    openReason,
		OpenCycleNum:  openCycleNum,
		CloseTime:     closeAction.Timestamp,
		ClosePrice:    closeAction.Price,
		CloseQuantity: closeAction.Quantity,
		CloseOrderID:  closeAction.OrderID,
		CloseReason:   closeReason,
		CloseCycleNum: int(closeCycleNum),
		IsForced:      isForced,
		ForcedReason:  forcedReason,
		Duration:      duration.String(),
		PositionValue: positionValue,
		MarginUsed:    marginUsed,
		PnL:           pnl,
		PnLPct:        pnlPct,
		WasStopLoss:   isForced && pnl < 0,
		Success:       openAction.Success && closeAction.Success,
		Error:         closeAction.Error,
	}
}

// GetID 获取trader ID
func (at *AutoTrader) GetID() string {
	return at.id
}

// GetName 获取trader名称
func (at *AutoTrader) GetName() string {
	return at.name
}

// GetAIModel 获取AI模型
func (at *AutoTrader) GetAIModel() string {
	return at.aiModel
}

// GetDecisionLogger 获取决策日志记录器（已移除文件日志）
// 注意：文件日志已移除，此方法已废弃，返回nil
// Deprecated: 文件日志已迁移到数据库存储，请使用 GetDecisionRecordsFromDB 等方法
func (at *AutoTrader) GetDecisionLogger() interface{} {
	return nil
}

// rollbackOrders 回滚订单（恢复旧的止损止盈订单）
func (at *AutoTrader) rollbackOrders(symbol, sideStr string, quantity, oldStopLoss, oldTakeProfit float64) error {
	var rollbackErrors []string
	
	// 恢复止损订单
	if oldStopLoss > 0 {
		if err := at.trader.SetStopLoss(symbol, sideStr, quantity, oldStopLoss); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Sprintf("恢复止损失败: %v", err))
		} else {
			log.Printf("  ✓ 已恢复止损订单: %.4f", oldStopLoss)
		}
	}
	
	// 恢复止盈订单
	if oldTakeProfit > 0 {
		if err := at.trader.SetTakeProfit(symbol, sideStr, quantity, oldTakeProfit); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Sprintf("恢复止盈失败: %v", err))
		} else {
			log.Printf("  ✓ 已恢复止盈订单: %.4f", oldTakeProfit)
		}
	}
	
	if len(rollbackErrors) > 0 {
		return fmt.Errorf("回滚部分失败: %s", strings.Join(rollbackErrors, "; "))
	}
	
	return nil
}

// GetDecisionRecordsFromDB 从数据库获取决策记录（用于API接口）
func (at *AutoTrader) GetDecisionRecordsFromDB(limit int) ([]*logger.DecisionRecord, error) {
	if at.storageAdapter == nil {
		return []*logger.DecisionRecord{}, nil
	}

	decisionStorage := at.storageAdapter.GetDecisionStorage()
	if decisionStorage == nil {
		return []*logger.DecisionRecord{}, nil
	}

	dbRecords, err := decisionStorage.GetLatestRecords(at.id, limit)
	if err != nil {
		return nil, fmt.Errorf("从数据库获取决策记录失败: %w", err)
	}

	// 转换为logger.DecisionRecord格式
	var records []*logger.DecisionRecord
	for _, dbRecord := range dbRecords {
		record := &logger.DecisionRecord{
			Timestamp:      dbRecord.Timestamp,
			CycleNumber:    dbRecord.CycleNumber,
			InputPrompt:    dbRecord.InputPrompt,
			CoTTrace:       dbRecord.CoTTrace,
			DecisionJSON:   dbRecord.DecisionJSON,
			Success:        dbRecord.Success,
			ErrorMessage:   dbRecord.ErrorMessage,
		}

		// 解析JSON字段
		if err := json.Unmarshal(dbRecord.AccountState, &record.AccountState); err != nil {
			log.Printf("⚠️  解析账户状态失败: %v", err)
		}
		if err := json.Unmarshal(dbRecord.Positions, &record.Positions); err != nil {
			log.Printf("⚠️  解析持仓失败: %v", err)
		}
		if err := json.Unmarshal(dbRecord.CandidateCoins, &record.CandidateCoins); err != nil {
			log.Printf("⚠️  解析候选币种失败: %v", err)
		}
		if err := json.Unmarshal(dbRecord.Decisions, &record.Decisions); err != nil {
			log.Printf("⚠️  解析决策失败: %v", err)
		}
		if err := json.Unmarshal(dbRecord.ExecutionLog, &record.ExecutionLog); err != nil {
			log.Printf("⚠️  解析执行日志失败: %v", err)
		}

		records = append(records, record)
	}

	return records, nil
}

// GetPerformanceFromDB 从数据库获取表现分析（用于API接口）
func (at *AutoTrader) GetPerformanceFromDB(lookbackCycles int) (*logger.PerformanceAnalysis, error) {
	if at.storageAdapter == nil {
		return &logger.PerformanceAnalysis{
			RecentTrades: []logger.TradeOutcome{},
			SymbolStats:  make(map[string]*logger.SymbolPerformance),
		}, nil
	}

	decisionStorage := at.storageAdapter.GetDecisionStorage()
	if decisionStorage == nil {
		return &logger.PerformanceAnalysis{
			RecentTrades: []logger.TradeOutcome{},
			SymbolStats:  make(map[string]*logger.SymbolPerformance),
		}, nil
	}

	records, err := decisionStorage.GetLatestRecords(at.id, lookbackCycles)
	if err != nil {
		return nil, fmt.Errorf("从数据库获取决策记录失败: %w", err)
	}

	// 使用已有的分析函数
	return at.analyzePerformanceFromDB(records), nil
}

// GetStatisticsFromDB 从数据库获取统计信息（用于API接口）
func (at *AutoTrader) GetStatisticsFromDB() (*logger.Statistics, error) {
	if at.storageAdapter == nil {
		return &logger.Statistics{}, nil
	}

	decisionStorage := at.storageAdapter.GetDecisionStorage()
	if decisionStorage == nil {
		return &logger.Statistics{}, nil
	}

	records, err := decisionStorage.GetLatestRecords(at.id, 10000)
	if err != nil {
		return nil, fmt.Errorf("从数据库获取决策记录失败: %w", err)
	}

	stats := &logger.Statistics{
		TotalCycles:        len(records),
		SuccessfulCycles:   0,
		FailedCycles:       0,
		TotalOpenPositions: 0,
		TotalClosePositions: 0,
	}

	// 统计决策记录
	for _, record := range records {
		if record.Success {
			stats.SuccessfulCycles++
		} else {
			stats.FailedCycles++
		}

		// 解析decisions字段，统计开仓和平仓操作
		var decisions []logger.DecisionAction
		if err := json.Unmarshal(record.Decisions, &decisions); err == nil {
			for _, action := range decisions {
				if !action.Success {
					continue
				}
				switch action.Action {
				case "open_long", "open_short":
					stats.TotalOpenPositions++
				case "close_long", "close_short":
					stats.TotalClosePositions++
				}
			}
		}
	}

	return stats, nil
}

// GetStatus 获取系统状态（用于API，带并发保护）
func (at *AutoTrader) GetStatus() map[string]interface{} {
	aiProvider := "DeepSeek"
	if at.config.UseQwen {
		aiProvider = "Qwen"
	}

	// 使用读锁保护共享状态
	at.riskMu.RLock()
	defer at.riskMu.RUnlock()

	return map[string]interface{}{
		"trader_id":       at.id,
		"trader_name":     at.name,
		"ai_model":        at.aiModel,
		"exchange":        at.exchange,
		"is_running":      atomic.LoadInt32(&at.isRunning) == 1,
		"start_time":      at.startTime.Format(time.RFC3339),
		"runtime_minutes": int(time.Since(at.startTime).Minutes()),
		"call_count":      atomic.LoadInt64(&at.callCount),
		"initial_balance": at.initialBalance,
		"scan_interval":   at.config.ScanInterval.String(),
		"stop_until":      at.stopUntil.Format(time.RFC3339),
		"last_reset_time": at.lastResetTime.Format(time.RFC3339),
		"ai_provider":     aiProvider,
	}
}

// GetAccountInfo 获取账户信息（用于API）
func (at *AutoTrader) GetAccountInfo() (map[string]interface{}, error) {
	balance, err := at.trader.GetBalance()
	if err != nil {
		return nil, fmt.Errorf("获取余额失败: %w", err)
	}

	// 获取账户字段
	totalWalletBalance := 0.0
	totalUnrealizedProfit := 0.0
	availableBalance := 0.0

	if wallet, ok := balance["totalWalletBalance"].(float64); ok {
		totalWalletBalance = wallet
	}
	if unrealized, ok := balance["totalUnrealizedProfit"].(float64); ok {
		totalUnrealizedProfit = unrealized
	}
	if avail, ok := balance["availableBalance"].(float64); ok {
		availableBalance = avail
	}

	// Total Equity = 钱包余额 + 未实现盈亏
	totalEquity := totalWalletBalance + totalUnrealizedProfit

	// 获取持仓计算总保证金
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	totalMarginUsed := 0.0
	totalUnrealizedPnL := 0.0
	for _, pos := range positions {
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		totalUnrealizedPnL += unrealizedPnl

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}
		marginUsed := (quantity * markPrice) / float64(leverage)
		totalMarginUsed += marginUsed
	}

	// 使用读锁保护共享状态（initialBalance和dailyPnL）
	at.riskMu.RLock()
	initialBalance := at.initialBalance
	dailyPnL := at.dailyPnL
	at.riskMu.RUnlock()

	totalPnL := totalEquity - initialBalance
	totalPnLPct := 0.0
	if initialBalance > 0 {
		totalPnLPct = (totalPnL / initialBalance) * 100
	}

	marginUsedPct := 0.0
	if totalEquity > 0 {
		marginUsedPct = (totalMarginUsed / totalEquity) * 100
	}

	return map[string]interface{}{
		// 核心字段
		"total_equity":      totalEquity,           // 账户净值 = wallet + unrealized
		"wallet_balance":    totalWalletBalance,    // 钱包余额（不含未实现盈亏）
		"unrealized_profit": totalUnrealizedProfit, // 未实现盈亏（从API）
		"available_balance": availableBalance,      // 可用余额

		// 盈亏统计
		"total_pnl":            totalPnL,           // 总盈亏 = equity - initial
		"total_pnl_pct":        totalPnLPct,        // 总盈亏百分比
		"total_unrealized_pnl": totalUnrealizedPnL, // 未实现盈亏（从持仓计算）
		"initial_balance":      initialBalance,      // 初始余额
		"daily_pnl":            dailyPnL,           // 日盈亏

		// 持仓信息
		"position_count":  len(positions),  // 持仓数量
		"margin_used":     totalMarginUsed, // 保证金占用
		"margin_used_pct": marginUsedPct,   // 保证金使用率
	}, nil
}

// GetPositions 获取持仓列表（用于API，包含逻辑信息）
func (at *AutoTrader) GetPositions() ([]map[string]interface{}, error) {
	positions, err := at.trader.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("获取持仓失败: %w", err)
	}

	var result []map[string]interface{}
	for _, pos := range positions {
		symbol := pos["symbol"].(string)
		side := pos["side"].(string)
		entryPrice := pos["entryPrice"].(float64)
		markPrice := pos["markPrice"].(float64)
		quantity := pos["positionAmt"].(float64)
		if quantity < 0 {
			quantity = -quantity
		}
		unrealizedPnl := pos["unRealizedProfit"].(float64)
		liquidationPrice := pos["liquidationPrice"].(float64)

		leverage := 10
		if lev, ok := pos["leverage"].(float64); ok {
			leverage = int(lev)
		}

		pnlPct := 0.0
		if side == "long" {
			pnlPct = ((markPrice - entryPrice) / entryPrice) * float64(leverage) * 100
		} else {
			pnlPct = ((entryPrice - markPrice) / entryPrice) * float64(leverage) * 100
		}

		marginUsed := (quantity * markPrice) / float64(leverage)

		// 加载持仓逻辑并检查是否失效
		logic := at.positionLogicManager.GetLogic(symbol, side)
		logicInvalid := false
		var invalidReasons []string
		
		if logic != nil {
			// 获取市场数据用于检查逻辑
			if marketData, err := market.Get(symbol); err == nil {
				ctx := &decision.Context{
					MultiTimeframeConfig: at.config.MultiTimeframeConfig,
					MarketDataMap:        make(map[string]*market.Data),
					StrategyName:         at.config.StrategyName,
					StrategyPreference:   at.config.StrategyPreference,
				}
				ctx.MarketDataMap[symbol] = marketData
				logicInvalid, invalidReasons = decision.CheckLogicValidity(logic, symbol, marketData, ctx, side)
			}
		}

		// 构建返回的持仓数据
		posData := map[string]interface{}{
			"symbol":             symbol,
			"side":               side,
			"entry_price":        entryPrice,
			"mark_price":         markPrice,
			"quantity":           quantity,
			"leverage":           leverage,
			"unrealized_pnl":     unrealizedPnl,
			"unrealized_pnl_pct": pnlPct,
			"liquidation_price":  liquidationPrice,
			"margin_used":        marginUsed,
		}

		// 添加逻辑信息
		if logic != nil {
			if logic.EntryLogic != nil {
				posData["entry_logic"] = logic.EntryLogic
			}
			if logic.ExitLogic != nil {
				posData["exit_logic"] = logic.ExitLogic
			}
		}
		if logicInvalid {
			posData["logic_invalid"] = true
			if len(invalidReasons) > 0 {
				posData["invalid_reasons"] = invalidReasons
			}
		}

		result = append(result, posData)
	}

	return result, nil
}

// sortDecisionsByPriority 对决策排序：先平仓，再开仓，最后hold/wait
// 这样可以避免换仓时仓位叠加超限
func sortDecisionsByPriority(decisions []decision.Decision) []decision.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// 定义优先级
	getActionPriority := func(action string) int {
		switch action {
		case "close_long", "close_short":
			return 1 // 最高优先级：先平仓
		case "open_long", "open_short":
			return 2 // 次优先级：后开仓
		case "hold", "wait":
			return 3 // 最低优先级：观望
		default:
			return 999 // 未知动作放最后
		}
	}

	// 复制决策列表
	sorted := make([]decision.Decision, len(decisions))
	copy(sorted, decisions)

	// 按优先级排序
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if getActionPriority(sorted[i].Action) > getActionPriority(sorted[j].Action) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

// deduplicateDecisions 去重决策：合并同一币种相同类型的操作
// 对于 update_sl 和 update_tp，只保留最后一个（按顺序）
func deduplicateDecisions(decisions []decision.Decision) []decision.Decision {
	if len(decisions) <= 1 {
		return decisions
	}

	// 用于跟踪每个币种+操作类型的最后出现的索引
	// key: symbol_action (如 "BTCUSDT_update_tp")
	lastIndexMap := make(map[string]int)
	
	// 需要去重的操作类型
	dedupActions := map[string]bool{
		"update_sl": true,
		"update_tp": true,
	}

	// 第一遍：找出每个币种+操作类型的最后一个索引
	for i, d := range decisions {
		if dedupActions[d.Action] {
			key := d.Symbol + "_" + d.Action
			lastIndexMap[key] = i
		}
	}

	// 第二遍：只保留每个币种+操作类型的最后一个
	result := make([]decision.Decision, 0, len(decisions))
	for i, d := range decisions {
		if dedupActions[d.Action] {
			key := d.Symbol + "_" + d.Action
			// 只保留最后一个
			if lastIndexMap[key] == i {
				result = append(result, d)
			} else {
				log.Printf("  ⏭️  跳过重复操作: %s %s (已合并到后续操作)", d.Symbol, d.Action)
			}
		} else {
			// 其他操作类型保留所有
			result = append(result, d)
		}
	}

	return result
}

// SyncManualTradesFromExchange 同步手工交易到历史记录
// 这个方法会从交易所获取最近的交易历史，并与本地记录对比，补充缺失的交易记录
func (at *AutoTrader) SyncManualTradesFromExchange() error {
	log.Println("🔄 开始同步交易所交易历史到本地记录...")
	
	// 检查trader是否支持GetAccountTrades方法
	asterTrader, ok := at.trader.(*AsterTrader)
	if !ok {
		return fmt.Errorf("当前交易器不支持获取交易历史功能")
	}
	
	// 获取最近7天的交易历史
	endTime := time.Now()
	startTime := endTime.AddDate(0, 0, -7) // 最近7天
	
	accountTrades, err := asterTrader.GetAccountTrades("", startTime, endTime, 1000)
	if err != nil {
		return fmt.Errorf("获取交易所交易历史失败: %w", err)
	}
	
	log.Printf("📊 从交易所获取到 %d 笔交易记录", len(accountTrades))
	
	if len(accountTrades) == 0 {
		log.Println("✅ 交易所没有新的交易记录")
		return nil
	}
	
	// 获取本地已存储的交易记录
	tradeStorage := at.storageAdapter.GetTradeStorage()
	if tradeStorage == nil {
		return fmt.Errorf("无法获取交易存储")
	}
	
	localTrades, err := tradeStorage.GetLatestTrades(1000) // 获取最近的1000条记录
	if err != nil {
		return fmt.Errorf("获取本地交易记录失败: %w", err)
	}
	
	// 创建本地交易的映射，用于快速查找（使用CloseOrderID作为键）
	localTradeMap := make(map[int64]bool)
	for _, trade := range localTrades {
		if trade.CloseOrderID > 0 {
			localTradeMap[trade.CloseOrderID] = true
		}
	}
	
	// 首先按订单ID聚合所有成交记录（同一订单可能有多个成交）
	type aggregatedTrade struct {
		orderId       int64
		symbol        string
		side          string
		tradeSide     string
		totalQty      float64
		totalPnL      float64
		weightedPrice float64 // 加权平均价格 = sum(price * qty) / sum(qty)
		firstTime     time.Time
		lastTime      time.Time
		totalRealizedPnl float64
	}
	
	// 按订单ID聚合交易（使用orderId作为键，因为同一订单可能有多个成交）
	orderMap := make(map[int64]*aggregatedTrade)
	
	for _, exchangeTrade := range accountTrades {
		// 安全解析字段，添加错误处理
		symbol, ok := exchangeTrade["symbol"].(string)
		if !ok || symbol == "" {
			continue
		}
		
		// 解析orderId（订单ID，不是成交ID）
		var orderId float64
		var orderIdOK bool
		// 优先使用orderId字段（订单ID）
		if id, ok := exchangeTrade["orderId"].(float64); ok {
			orderId = id
			orderIdOK = true
		} else if id, ok := exchangeTrade["orderId"].(string); ok {
			// 也可能是字符串格式
			if parsed, err := strconv.ParseFloat(id, 64); err == nil {
				orderId = parsed
				orderIdOK = true
			}
		}
		
		if !orderIdOK || orderId == 0 {
			continue // 跳过没有orderId的记录
		}
		
		orderIdInt64 := int64(orderId)
		
		// 检查是否已存在
		if localTradeMap[orderIdInt64] {
			continue // 已存在，跳过
		}
		
		// 解析其他字段
		side, _ := exchangeTrade["side"].(string)
		timeMs, ok := exchangeTrade["time"].(float64)
		if !ok {
			if t, ok := exchangeTrade["timestamp"].(float64); ok {
				timeMs = t
			} else {
				continue
			}
		}
		
		// 解析价格和数量
		priceStr, ok := exchangeTrade["price"].(string)
		if !ok || priceStr == "" {
			continue
		}
		price, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			continue
		}
		
		qtyStr, ok := exchangeTrade["qty"].(string)
		if !ok {
			qtyStr, _ = exchangeTrade["quantity"].(string)
		}
		if qtyStr == "" {
			continue
		}
		qty, err := strconv.ParseFloat(qtyStr, 64)
		if err != nil {
			continue
		}
		
		// 解析realizedPnl - 这是判断是否为平仓的关键字段
		realizedPnlStr, _ := exchangeTrade["realizedPnl"].(string)
		realizedPnl, _ := strconv.ParseFloat(realizedPnlStr, 64)
		
		// 将时间戳转换为time.Time（自动检测是秒还是毫秒）
		// 如果时间戳小于 1e12，认为是秒；否则认为是毫秒
		var tradeTime time.Time
		if timeMs < 1e12 {
			// 时间戳是秒，转换为毫秒
			tradeTime = time.Unix(int64(timeMs), 0)
		} else {
			// 时间戳是毫秒
			tradeTime = time.UnixMilli(int64(timeMs))
		}
		
		// 判断是否为平仓操作：realizedPnl != 0 通常表示平仓
		if realizedPnl == 0 {
			continue // 跳过开仓或调整仓位
		}
		
		// 确定交易方向
		var tradeSide string
		sideUpper := strings.ToUpper(side)
		if sideUpper == "SELL" {
			tradeSide = "long"
		} else if sideUpper == "BUY" {
			tradeSide = "short"
		} else {
			continue // 无效的方向
		}
		
		// 聚合到订单
		if agg, exists := orderMap[orderIdInt64]; exists {
			// 已存在，累加
			// 更新加权平均价格（先计算，再更新数量）
			oldTotalValue := agg.weightedPrice * agg.totalQty
			newTotalValue := oldTotalValue + price*qty
			agg.totalQty += qty
			agg.weightedPrice = newTotalValue / agg.totalQty
			
			agg.totalPnL += realizedPnl
			agg.totalRealizedPnl += realizedPnl
			
			if tradeTime.Before(agg.firstTime) {
				agg.firstTime = tradeTime
			}
			if tradeTime.After(agg.lastTime) {
				agg.lastTime = tradeTime
			}
		} else {
			// 新建聚合记录
			orderMap[orderIdInt64] = &aggregatedTrade{
				orderId:          orderIdInt64,
				symbol:           symbol,
				side:             side,
				tradeSide:        tradeSide,
				totalQty:         qty,
				totalPnL:         realizedPnl,
				weightedPrice:    price,
				firstTime:        tradeTime,
				lastTime:         tradeTime,
				totalRealizedPnl: realizedPnl,
			}
		}
	}
	
	// 将聚合后的订单转换为交易记录
	var missingTrades []*storage.TradeRecord
	for _, agg := range orderMap {
		
		// 查找对应的开仓信息
		// 注意：Decision结构中没有Price、Quantity等字段，需要从其他来源获取
		var openPrice, openQuantity float64
		var openLeverage int
		var openOrderID int64
		var openTime time.Time
		
		// 尝试从交易所历史中查找对应的开仓交易（优先使用交易所数据，更准确）
		// 查找方向相反且realizedPnl为0的交易（开仓），且时间早于平仓时间
		var bestOpenTrade map[string]interface{}
		var bestOpenTime time.Time
		for _, potentialOpenTrade := range accountTrades {
			openTradeSymbol, ok := potentialOpenTrade["symbol"].(string)
			if !ok || openTradeSymbol != agg.symbol {
				continue
			}
			
			openTradeSide, _ := potentialOpenTrade["side"].(string)
			openTradeRealizedPnlStr, _ := potentialOpenTrade["realizedPnl"].(string)
			openTradeRealizedPnlVal, _ := strconv.ParseFloat(openTradeRealizedPnlStr, 64)
			openTradeTimeMs, ok := potentialOpenTrade["time"].(float64)
			if !ok {
				if t, ok := potentialOpenTrade["timestamp"].(float64); ok {
					openTradeTimeMs = t
				} else {
					continue
				}
			}
			// 自动检测时间戳是秒还是毫秒
			var openTradeTime time.Time
			if openTradeTimeMs < 1e12 {
				openTradeTime = time.Unix(int64(openTradeTimeMs), 0)
			} else {
				openTradeTime = time.UnixMilli(int64(openTradeTimeMs))
			}
			
			// 开仓交易：方向相反、realizedPnl为0、时间早于平仓时间
			isOppositeSide := (agg.tradeSide == "long" && strings.ToUpper(openTradeSide) == "BUY") ||
				(agg.tradeSide == "short" && strings.ToUpper(openTradeSide) == "SELL")
			
			// 找到符合条件的开仓交易，且时间早于平仓时间（使用lastTime作为平仓时间）
			if isOppositeSide && openTradeRealizedPnlVal == 0 && openTradeTime.Before(agg.lastTime) {
				// 选择最接近平仓时间的开仓交易（时间最大的，但早于平仓时间）
				if bestOpenTrade == nil || openTradeTime.After(bestOpenTime) {
					bestOpenTrade = potentialOpenTrade
					bestOpenTime = openTradeTime
				}
			}
		}
		
		// 如果从交易所历史找到了开仓交易
		if bestOpenTrade != nil {
			if p, ok := bestOpenTrade["price"].(string); ok {
				openPrice, _ = strconv.ParseFloat(p, 64)
			}
			if q, ok := bestOpenTrade["qty"].(string); ok {
				openQuantity, _ = strconv.ParseFloat(q, 64)
			}
			openTime = bestOpenTime
			if id, ok := bestOpenTrade["orderId"].(float64); ok {
				openOrderID = int64(id)
			}
			
			// 尝试获取杠杆：优先从当前持仓信息获取（如果该持仓还存在）
			// 如果持仓已平仓，则从本地交易历史中查找
			openLeverage = 0
			positions, err := at.trader.GetPositions()
			if err == nil {
				for _, pos := range positions {
					if posSymbol, ok := pos["symbol"].(string); ok && posSymbol == agg.symbol {
						if posSide, ok := pos["side"].(string); ok && posSide == agg.tradeSide {
							if lev, ok := pos["leverage"].(float64); ok {
								openLeverage = int(lev)
								break
							}
						}
					}
				}
			}
			
			// 如果从持仓信息获取不到，尝试从本地交易历史中查找
			if openLeverage == 0 && at.storageAdapter != nil {
				tradeStorage := at.storageAdapter.GetTradeStorage()
				if tradeStorage != nil {
					localTrades, err := tradeStorage.GetLatestTrades(500)
					if err == nil {
						for _, trade := range localTrades {
							if trade.Symbol == agg.symbol && trade.Side == agg.tradeSide {
								// 找到匹配的开仓记录，且开仓时间接近
								if trade.OpenTime.Before(agg.lastTime) && 
								   trade.OpenTime.After(agg.lastTime.Add(-24*time.Hour)) {
									openLeverage = trade.OpenLeverage
									break
								}
							}
						}
					}
				}
			}
			
			// 如果还是获取不到，使用配置的杠杆（根据币种类型）
			if openLeverage == 0 {
				if agg.symbol == "BTCUSDT" || agg.symbol == "ETHUSDT" {
					openLeverage = at.config.BTCETHLeverage
				} else {
					openLeverage = at.config.AltcoinLeverage
				}
				log.Printf("⚠️  无法获取 %s %s 的实际杠杆，使用配置的杠杆: %dx", 
					agg.symbol, agg.tradeSide, openLeverage)
			}
			
			log.Printf("✅ 从交易所历史中找到 %s %s 的开仓交易 (开仓时间: %s, 平仓时间: %s, 杠杆: %dx)", 
				agg.symbol, agg.tradeSide, 
				openTime.Format("2006-01-02 15:04:05"), 
				agg.lastTime.Format("2006-01-02 15:04:05"),
				openLeverage)
		}
		
		// 如果从交易所历史找不到，尝试从本地交易历史中查找
		if openPrice == 0 && at.storageAdapter != nil {
			tradeStorage := at.storageAdapter.GetTradeStorage()
			if tradeStorage != nil {
				localTrades, err := tradeStorage.GetLatestTrades(500) // 增加查找数量
				if err == nil {
					// 查找最近的一次开仓交易，且开仓时间早于平仓时间
					var bestLocalTrade *storage.TradeRecord
					var bestLocalOpenTime time.Time
					for _, trade := range localTrades {
						if trade.Symbol == agg.symbol && trade.Side == agg.tradeSide {
							// 确保开仓时间早于平仓时间（使用lastTime作为平仓时间）
							if trade.OpenTime.Before(agg.lastTime) {
								// 选择最接近平仓时间的开仓记录（时间最大的，但早于平仓时间）
								if bestLocalTrade == nil || trade.OpenTime.After(bestLocalOpenTime) {
									bestLocalTrade = trade
									bestLocalOpenTime = trade.OpenTime
								}
							}
						}
					}
					
					if bestLocalTrade != nil {
						openPrice = bestLocalTrade.OpenPrice
						openQuantity = bestLocalTrade.OpenQuantity
						openLeverage = bestLocalTrade.OpenLeverage
						openOrderID = bestLocalTrade.OpenOrderID
						openTime = bestLocalTrade.OpenTime
						log.Printf("✅ 从本地历史中找到 %s %s 的开仓交易 (开仓时间: %s, 平仓时间: %s)", 
							agg.symbol, agg.tradeSide,
							openTime.Format("2006-01-02 15:04:05"),
							agg.lastTime.Format("2006-01-02 15:04:05"))
					}
				}
			}
		}
		
		// 如果还是找不到，跳过这条记录（不记录错误的交易）
		if openPrice == 0 {
			log.Printf("⚠️  无法找到 %s %s 的开仓交易，跳过此记录（平仓时间: %s）", 
				agg.symbol, agg.tradeSide, agg.lastTime.Format("2006-01-02 15:04:05"))
			continue // 跳过这条记录，不保存到数据库
		}
		
		// 构建交易ID - 使用订单ID作为唯一标识（同一订单的所有成交合并为一个记录）
		tradeId := fmt.Sprintf("%s_%s_%d", agg.symbol, agg.tradeSide, agg.orderId)
		
		// 计算持仓时长
		duration := agg.lastTime.Sub(openTime)
		
		// 使用聚合后的盈亏
		calculatedPnL := agg.totalRealizedPnl
		
		// 计算持仓价值和保证金
		positionValue := openQuantity * openPrice
		marginUsed := positionValue / float64(openLeverage)
		pnlPct := 0.0
		if marginUsed > 0 {
			pnlPct = (calculatedPnL / marginUsed) * 100
		}
		
		// 创建完整的交易记录（使用聚合后的数据）
		tradeRecord := &storage.TradeRecord{
			TradeID:        tradeId,
			Symbol:         agg.symbol,
			Side:           agg.tradeSide,
			OpenTime:       openTime,
			OpenPrice:      openPrice,
			OpenQuantity:   openQuantity,
			OpenLeverage:   openLeverage,
			OpenOrderID:    openOrderID,
			OpenReason:     "系统外开仓",
			OpenCycleNum:   0,
			CloseTime:      agg.lastTime, // 使用最后成交时间
			ClosePrice:     agg.weightedPrice, // 使用加权平均价格
			CloseQuantity:  agg.totalQty, // 使用总数量
			CloseOrderID:   agg.orderId,
			CloseReason:    "手动平仓",
			CloseCycleNum:  int(atomic.LoadInt64(&at.callCount)),
			IsForced:       false,
			ForcedReason:   "",
			Duration:       duration.String(),
			PositionValue:  positionValue,
			MarginUsed:     marginUsed,
			PnL:            calculatedPnL,
			PnLPct:         pnlPct,
			WasStopLoss:    false,
			Success:        true,
			Error:          "",
		}
		
		missingTrades = append(missingTrades, tradeRecord)
	}
	
	// 保存缺失的交易记录
	syncedCount := 0
	for _, trade := range missingTrades {
		if err := tradeStorage.LogTrade(trade); err != nil {
			log.Printf("⚠️  保存缺失交易记录失败: %v, ID: %s", err, trade.TradeID)
			continue
		}
		syncedCount++
		log.Printf("✅ 已同步缺失交易: %s - %s, 盈亏: %.2f USDT (%.2f%%)", trade.Symbol, trade.Side, trade.PnL, trade.PnLPct)
	}
	
	log.Printf("✅ 交易同步完成: 找到 %d 个缺失交易，成功同步 %d 个", len(missingTrades), syncedCount)
	return nil
}

// findLatestOpenDecision 查找最近的开仓决策记录
func (at *AutoTrader) findLatestOpenDecision(symbol, side string) (*decision.Decision, time.Time, error) {
	if at.storageAdapter == nil {
		return nil, time.Time{}, fmt.Errorf("storage adapter is nil")
	}
	
	decisionStorage := at.storageAdapter.GetDecisionStorage()
	if decisionStorage == nil {
		return nil, time.Time{}, fmt.Errorf("decision storage is nil")
	}
	
	// 获取最近的决策记录 - 使用正确的函数名GetLatestRecords
	records, err := decisionStorage.GetLatestRecords(at.id, 100) // 查找最近100条记录
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("获取决策记录失败: %w", err)
	}
	
	// 从最新的记录开始向前查找
	for i := len(records) - 1; i >= 0; i-- {
		var decisionsList []decision.Decision
		if err := json.Unmarshal(records[i].Decisions, &decisionsList); err == nil {
			for _, d := range decisionsList {
				// 检查是否为匹配的开仓操作
				isMatch := d.Symbol == symbol && 
					((side == "long" && (d.Action == "open_long" || (strings.Contains(d.Action, "long") && !strings.Contains(d.Action, "close")))) ||
					 (side == "short" && (d.Action == "open_short" || (strings.Contains(d.Action, "short") && !strings.Contains(d.Action, "close")))))
				
				if isMatch {
					// 查找开仓价格和数量
					if d.Action == "open_long" || d.Action == "open_short" {
						return &d, records[i].Timestamp, nil
					}
				}
			}
		}
	}
	
	return nil, time.Time{}, fmt.Errorf("未找到 %s %s 的开仓记录", symbol, side)
}

// getEntryInfoFromHistory 从历史记录中获取开仓信息
// 返回: (entryPrice, quantity, leverage)
// 注意：Decision结构中没有Price、Quantity等字段，所以只能从本地交易历史中查找
func (at *AutoTrader) getEntryInfoFromHistory(symbol, side string) (float64, float64, int) {
	// 从本地交易历史中查找
	if at.storageAdapter != nil {
		tradeStorage := at.storageAdapter.GetTradeStorage()
		if tradeStorage != nil {
			// 查找该币种最近的交易记录
			localTrades, err := tradeStorage.GetLatestTrades(100)
			if err == nil {
				// 查找匹配的开仓交易（未平仓的或最近的）
				for _, trade := range localTrades {
					if trade.Symbol == symbol && trade.Side == side {
						// 找到匹配的交易，返回开仓信息
						return trade.OpenPrice, trade.OpenQuantity, trade.OpenLeverage
					}
				}
			}
		}
	}
	
	// 如果都找不到，返回0值（调用方需要处理）
	return 0, 0, 0
}

// getLatestClosePrice 获取最近的平仓价格
func (at *AutoTrader) getLatestClosePrice(symbol, side string) (float64, error) {
	// 尝试从交易所直接获取最近的交易信息
	// 检查trader是否支持GetAccountTrades方法
	asterTrader, ok := at.trader.(*AsterTrader)
	if !ok {
		return 0, fmt.Errorf("当前交易器不支持获取交易历史功能")
	}
	
	// 获取最近24小时的交易历史
	endTime := time.Now()
	startTime := endTime.Add(-24 * time.Hour) // 最近24小时
	
	accountTrades, err := asterTrader.GetAccountTrades(symbol, startTime, endTime, 100)
	if err != nil {
		return 0, fmt.Errorf("获取交易所交易历史失败: %w", err)
	}
	
	// 收集所有匹配的平仓交易，然后找到时间最新的
	type closingTrade struct {
		price     float64
		timestamp int64
	}
	var closingTrades []closingTrade
	
	for _, trade := range accountTrades {
		tradeSymbol, ok := trade["symbol"].(string)
		if !ok || tradeSymbol != symbol {
			continue
		}
		
		tradeSide, ok := trade["side"].(string)
		if !ok {
			continue
		}
		
		// 检查realizedPnl判断是否为平仓
		realizedPnlStr, _ := trade["realizedPnl"].(string)
		realizedPnl, _ := strconv.ParseFloat(realizedPnlStr, 64)
		
		// 判断是否是对应方向的平仓操作
		isClosing := false
		if side == "long" && strings.ToUpper(tradeSide) == "SELL" && realizedPnl != 0 {
			isClosing = true // 多头平仓
		} else if side == "short" && strings.ToUpper(tradeSide) == "BUY" && realizedPnl != 0 {
			isClosing = true // 空头平仓（反向操作）
		}
		
		if isClosing {
			priceStr, ok := trade["price"].(string)
			if !ok {
				continue
			}
			
			price, err := strconv.ParseFloat(priceStr, 64)
			if err != nil {
				continue
			}
			
			// 获取时间戳
			timeMs, ok := trade["time"].(float64)
			if !ok {
				if t, ok := trade["timestamp"].(float64); ok {
					timeMs = t
				} else {
					continue
				}
			}
			
			closingTrades = append(closingTrades, closingTrade{
				price:     price,
				timestamp: int64(timeMs),
			})
		}
	}
	
	// 如果没有找到任何平仓交易
	if len(closingTrades) == 0 {
		return 0, fmt.Errorf("未找到 %s %s 的平仓记录", symbol, side)
	}
	
	// 找到时间戳最大的（最新的）平仓交易
	var latestTrade closingTrade
	for _, ct := range closingTrades {
		if ct.timestamp > latestTrade.timestamp {
			latestTrade = ct
		}
	}
	
	return latestTrade.price, nil
}
