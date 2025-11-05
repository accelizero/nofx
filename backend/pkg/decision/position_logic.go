package decision

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"backend/pkg/market"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PositionLogic 持仓逻辑（进场和出场）
type PositionLogic struct {
	EntryLogic *EntryLogic `json:"entry_logic"` // 进场逻辑
	ExitLogic  *ExitLogic  `json:"exit_logic"`  // 出场逻辑
	StopLoss   float64     `json:"stop_loss,omitempty"`   // 当前设置的止损价格（与逻辑一起持久化）
	TakeProfit float64     `json:"take_profit,omitempty"` // 当前设置的止盈价格（与逻辑一起持久化）
}

// EntryLogic 进场逻辑
type EntryLogic struct {
	Reasoning     string                 `json:"reasoning"`      // AI的推理文本
	Conditions    []LogicCondition       `json:"conditions"`    // 结构化条件（预留字段，当前未使用，后续可扩展为更智能的条件提取）
	MultiTimeframe *MultiTimeframeLogic  `json:"multi_timeframe,omitempty"` // 多时间框架逻辑（如果使用）
	Timestamp     time.Time              `json:"timestamp"`     // 记录时间
}

// ExitLogic 出场逻辑
type ExitLogic struct {
	Reasoning     string                 `json:"reasoning"`      // AI的推理文本
	Conditions    []LogicCondition       `json:"conditions"`    // 结构化条件（预留字段，当前未使用，后续可扩展为更智能的条件提取）
	MultiTimeframe *MultiTimeframeLogic  `json:"multi_timeframe,omitempty"` // 多时间框架逻辑（如果使用）
	Timestamp     time.Time              `json:"timestamp"`     // 记录时间
}

// LogicCondition 逻辑条件
type LogicCondition struct {
	Type        string  `json:"type"`        // "trend", "momentum", "support_resistance", "indicator", "custom"
	Description string  `json:"description"` // 条件描述
	Timeframe   string  `json:"timeframe,omitempty"` // 时间框架（如"1d", "4h", "1h", "15m"）
	Value        float64 `json:"value,omitempty"`     // 阈值（如果适用）
	Operator     string  `json:"operator,omitempty"` // 操作符（">", "<", "==", "cross"等）
}

// MultiTimeframeLogic 多时间框架逻辑
type MultiTimeframeLogic struct {
	MajorTrend    string            `json:"major_trend"`    // 大周期趋势方向（"long"/"short"/"neutral"）
	PullbackEntry bool              `json:"pullback_entry"` // 是否使用回调入场策略
	Timeframes    map[string]string `json:"timeframes"`    // 各时间框架的状态描述
}

// PositionLogicManager 持仓逻辑管理器（负责保存和加载）
type PositionLogicManager struct {
	logDir string
	cache  map[string]*PositionLogic // symbol_side -> PositionLogic
	mu     sync.RWMutex
}

// NewPositionLogicManager 创建持仓逻辑管理器
func NewPositionLogicManager(logDir string) *PositionLogicManager {
	manager := &PositionLogicManager{
		logDir: logDir,
		cache:  make(map[string]*PositionLogic),
	}
	
	// 确保目录存在
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("⚠️  创建持仓逻辑目录失败: %v", err)
	}
	
	// 加载已存在的逻辑
	manager.loadAllLogics()
	
	return manager
}

// SaveEntryLogic 保存进场逻辑
func (plm *PositionLogicManager) SaveEntryLogic(symbol, side string, entryLogic *EntryLogic) error {
	posKey := symbol + "_" + side
	
	plm.mu.Lock()
	defer plm.mu.Unlock()
	
	// 获取或创建逻辑
	logic, exists := plm.cache[posKey]
	if !exists {
		logic = &PositionLogic{}
		plm.cache[posKey] = logic
	}
	
	logic.EntryLogic = entryLogic
	
	// 保存到文件
	return plm.saveToFile(posKey, logic)
}

// SaveExitLogic 保存出场逻辑
func (plm *PositionLogicManager) SaveExitLogic(symbol, side string, exitLogic *ExitLogic) error {
	posKey := symbol + "_" + side
	
	plm.mu.Lock()
	defer plm.mu.Unlock()
	
	// 获取或创建逻辑
	logic, exists := plm.cache[posKey]
	if !exists {
		logic = &PositionLogic{}
		plm.cache[posKey] = logic
	}
	
	logic.ExitLogic = exitLogic
	
	// 保存到文件
	return plm.saveToFile(posKey, logic)
}

// GetLogic 获取持仓逻辑
func (plm *PositionLogicManager) GetLogic(symbol, side string) *PositionLogic {
	posKey := symbol + "_" + side
	
	plm.mu.RLock()
	defer plm.mu.RUnlock()
	
	return plm.cache[posKey]
}

// SaveStopLoss 保存止损价格（与逻辑一起持久化）
func (plm *PositionLogicManager) SaveStopLoss(symbol, side string, stopLoss float64) error {
	posKey := symbol + "_" + side
	
	plm.mu.Lock()
	defer plm.mu.Unlock()
	
	// 获取或创建逻辑
	logic, exists := plm.cache[posKey]
	if !exists {
		logic = &PositionLogic{}
		plm.cache[posKey] = logic
	}
	
	logic.StopLoss = stopLoss
	
	// 保存到文件
	return plm.saveToFile(posKey, logic)
}

// SaveTakeProfit 保存止盈价格（与逻辑一起持久化）
func (plm *PositionLogicManager) SaveTakeProfit(symbol, side string, takeProfit float64) error {
	posKey := symbol + "_" + side
	
	plm.mu.Lock()
	defer plm.mu.Unlock()
	
	// 获取或创建逻辑
	logic, exists := plm.cache[posKey]
	if !exists {
		logic = &PositionLogic{}
		plm.cache[posKey] = logic
	}
	
	logic.TakeProfit = takeProfit
	
	// 保存到文件
	return plm.saveToFile(posKey, logic)
}

// SaveStopLossAndTakeProfit 同时保存止损和止盈价格（与逻辑一起持久化）
// 参数说明：
//   - stopLoss: 如果 > 0，则更新止损价格；如果 = 0，则保持原有值（不更新）
//   - takeProfit: 如果 > 0，则更新止盈价格；如果 = 0，则保持原有值（不更新）
// 这样设计是为了支持部分更新（例如只更新止盈，保持止损不变）
func (plm *PositionLogicManager) SaveStopLossAndTakeProfit(symbol, side string, stopLoss, takeProfit float64) error {
	posKey := symbol + "_" + side
	
	plm.mu.Lock()
	defer plm.mu.Unlock()
	
	// 获取或创建逻辑
	logic, exists := plm.cache[posKey]
	if !exists {
		logic = &PositionLogic{}
		plm.cache[posKey] = logic
	}
	
	// 只更新提供的价格（>0），否则保持原有值
	// 这样可以支持部分更新：例如只更新止盈（takeProfit > 0, stopLoss = 0），保持止损不变
	if stopLoss > 0 {
		logic.StopLoss = stopLoss
	}
	if takeProfit > 0 {
		logic.TakeProfit = takeProfit
	}
	
	// 保存到文件
	return plm.saveToFile(posKey, logic)
}

// DeleteLogic 删除持仓逻辑（平仓后调用）
func (plm *PositionLogicManager) DeleteLogic(symbol, side string) error {
	posKey := symbol + "_" + side
	
	plm.mu.Lock()
	defer plm.mu.Unlock()
	
	delete(plm.cache, posKey)
	
	// 删除文件
	filePath := filepath.Join(plm.logDir, posKey+".json")
	return os.Remove(filePath)
}

// saveToFile 保存逻辑到文件
func (plm *PositionLogicManager) saveToFile(posKey string, logic *PositionLogic) error {
	filePath := filepath.Join(plm.logDir, posKey+".json")
	
	data, err := json.MarshalIndent(logic, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化逻辑失败: %w", err)
	}
	
	if err := ioutil.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	
	return nil
}

// loadAllLogics 加载所有已保存的逻辑
func (plm *PositionLogicManager) loadAllLogics() {
	files, err := filepath.Glob(filepath.Join(plm.logDir, "*_*.json"))
	if err != nil {
		log.Printf("⚠️  加载持仓逻辑失败: %v", err)
		return
	}
	
	for _, file := range files {
		posKey := filepath.Base(file)
		posKey = posKey[:len(posKey)-5] // 移除".json"后缀
		
		data, err := ioutil.ReadFile(file)
		if err != nil {
			log.Printf("⚠️  读取逻辑文件失败 %s: %v", file, err)
			continue
		}
		
		var logic PositionLogic
		if err := json.Unmarshal(data, &logic); err != nil {
			log.Printf("⚠️  解析逻辑文件失败 %s: %v", file, err)
			continue
		}
		
		plm.cache[posKey] = &logic
	}
}

// ExtractEntryLogicFromReasoning 从AI的推理文本中提取进场逻辑
func ExtractEntryLogicFromReasoning(reasoning string, ctx *Context, symbol string) *EntryLogic {
	logic := &EntryLogic{
		Reasoning:  reasoning,
		Timestamp: time.Now(),
		Conditions: []LogicCondition{},
	}
	
	// 如果有多时间框架配置，提取多时间框架逻辑
	if ctx.MultiTimeframeConfig != nil {
		logic.MultiTimeframe = extractMultiTimeframeLogic(ctx, symbol, "entry")
	}
	
	// 提取结构化条件（预留功能）
	// 当前实现仅保存原始推理文本，Conditions字段为空数组
	// 后续可以扩展为更智能的条件提取，如从reasoning中解析出具体的价格、指标条件等
	
	return logic
}

// ExtractExitLogicFromReasoning 从AI的推理文本中提取出场逻辑
func ExtractExitLogicFromReasoning(reasoning string, ctx *Context, symbol string) *ExitLogic {
	logic := &ExitLogic{
		Reasoning:  reasoning,
		Timestamp: time.Now(),
		Conditions: []LogicCondition{},
	}
	
	// 如果有多时间框架配置，提取多时间框架逻辑
	if ctx.MultiTimeframeConfig != nil {
		logic.MultiTimeframe = extractMultiTimeframeLogic(ctx, symbol, "exit")
	}
	
	return logic
}

// extractMultiTimeframeLogic 提取多时间框架逻辑
func extractMultiTimeframeLogic(ctx *Context, symbol string, logicType string) *MultiTimeframeLogic {
	mtfLogic := &MultiTimeframeLogic{
		Timeframes: make(map[string]string),
	}
	
	// 获取市场数据
	marketData, exists := ctx.MarketDataMap[symbol]
	if !exists {
		return mtfLogic
	}
	
	// 分析大周期趋势
	// 使用EMA20和MACD判断趋势，增加阈值以避免噪音
	if marketData.CurrentEMA20 > 0 && marketData.CurrentPrice > 0 {
		// 计算价格与EMA20的相对偏差（百分比）
		emaRatio := (marketData.CurrentPrice - marketData.CurrentEMA20) / marketData.CurrentEMA20
		
		// 价格在EMA20上方（考虑0.1%的阈值，避免边界噪音）
		priceAboveEMA := emaRatio > 0.001
		// 价格在EMA20下方（考虑0.1%的阈值）
		priceBelowEMA := emaRatio < -0.001
		
		// MACD阈值：使用绝对值阈值避免接近0时的噪音（例如：0.0001可能只是计算误差）
		// MACD HIST通常在较大数值时才有意义，使用价格相对比例作为阈值更合理
		macdThreshold := marketData.CurrentPrice * 0.00001 // 价格的0.001%作为MACD阈值
		if macdThreshold < 1.0 {
			macdThreshold = 1.0 // 最小阈值1.0（对于BTC等大价格）
		}
		
		macdPositive := marketData.CurrentMACD > macdThreshold
		macdNegative := marketData.CurrentMACD < -macdThreshold
		
		// 判断趋势：两个条件必须同时满足，增加稳定性
		if priceAboveEMA && macdPositive {
			mtfLogic.MajorTrend = "long"
		} else if priceBelowEMA && macdNegative {
			mtfLogic.MajorTrend = "short"
		} else {
			// 其他情况：价格接近EMA、MACD接近0、或者信号不一致，都视为neutral
			mtfLogic.MajorTrend = "neutral"
		}
	}
	
	// 记录各时间框架状态（如果有多时间框架数据）
	// 这里可以扩展为更详细的逻辑提取
	
	return mtfLogic
}

// CheckLogicValidity 检查逻辑是否失效
// side: 持仓方向 "long" 或 "short"，用于正确判断趋势变化是否导致逻辑失效
// 返回：是否失效 + 失效原因列表
func CheckLogicValidity(logic *PositionLogic, symbol string, marketData *market.Data, ctx *Context, side string) (bool, []string) {
	var invalidReasons []string
	
	if logic == nil {
		return true, []string{"逻辑不存在"}
	}
	
	// 检查进场逻辑
	if logic.EntryLogic != nil {
		// 检查多时间框架逻辑
		if logic.EntryLogic.MultiTimeframe != nil {
			invalidReasons = append(invalidReasons, checkMultiTimeframeLogic(logic.EntryLogic.MultiTimeframe, symbol, marketData, ctx, side, "进场")...)
		}
		
		// 检查其他条件
		// 这里可以扩展为更详细的检查
	}
	
	// 检查出场逻辑
	if logic.ExitLogic != nil {
		// 检查多时间框架逻辑
		if logic.ExitLogic.MultiTimeframe != nil {
			invalidReasons = append(invalidReasons, checkMultiTimeframeLogic(logic.ExitLogic.MultiTimeframe, symbol, marketData, ctx, side, "出场")...)
		}
	}
	
	// 去重：如果进场和出场逻辑的趋势变化相同，只显示一次
	invalidReasons = deduplicateReasons(invalidReasons)
	
	return len(invalidReasons) > 0, invalidReasons
}

// deduplicateReasons 去重失效原因（如果有多条相同的趋势变化提示，只保留一条）
func deduplicateReasons(reasons []string) []string {
	seen := make(map[string]bool)
	var unique []string
	for _, reason := range reasons {
		if !seen[reason] {
			seen[reason] = true
			unique = append(unique, reason)
		}
	}
	return unique
}

// checkMultiTimeframeLogic 检查多时间框架逻辑
// side: 持仓方向 "long" 或 "short"
// logicType: "进场" 或 "出场"，用于错误提示
func checkMultiTimeframeLogic(mtfLogic *MultiTimeframeLogic, symbol string, marketData *market.Data, ctx *Context, side string, logicType string) []string {
	var invalidReasons []string
	
	// 检查大周期趋势是否改变
	if mtfLogic.MajorTrend != "" {
		currentMajorTrend := "neutral"
		
		if marketData.CurrentEMA20 > 0 && marketData.CurrentPrice > 0 {
			// 使用与extractMultiTimeframeLogic相同的判断逻辑，确保一致性
			// 计算价格与EMA20的相对偏差（百分比）
			emaRatio := (marketData.CurrentPrice - marketData.CurrentEMA20) / marketData.CurrentEMA20
			
			// 价格在EMA20上方（考虑0.1%的阈值，避免边界噪音）
			priceAboveEMA := emaRatio > 0.001
			// 价格在EMA20下方（考虑0.1%的阈值）
			priceBelowEMA := emaRatio < -0.001
			
			// MACD阈值：使用绝对值阈值避免接近0时的噪音
			macdThreshold := marketData.CurrentPrice * 0.00001 // 价格的0.001%作为MACD阈值
			if macdThreshold < 1.0 {
				macdThreshold = 1.0 // 最小阈值1.0（对于BTC等大价格）
			}
			
			macdPositive := marketData.CurrentMACD > macdThreshold
			macdNegative := marketData.CurrentMACD < -macdThreshold
			
			// 判断趋势：两个条件必须同时满足，增加稳定性
			if priceAboveEMA && macdPositive {
				currentMajorTrend = "long"
			} else if priceBelowEMA && macdNegative {
				currentMajorTrend = "short"
			}
			// 否则保持neutral（默认值）
		}
		
		// 只有当趋势变化与持仓方向相反时，才判定逻辑失效
		// 例如：如果持仓是long，只有趋势变为short时才失效
		// 如果持仓是short，只有趋势变为long时才失效
		// 趋势从neutral变为与持仓方向一致的趋势，不应该失效
		trendChanged := mtfLogic.MajorTrend != currentMajorTrend
		
		if trendChanged {
			// 定义趋势的中文化映射
			trendNameMap := map[string]string{
				"long":    "多头",
				"short":   "空头",
				"neutral": "中性",
			}
			
			originalTrendCN := trendNameMap[mtfLogic.MajorTrend]
			if originalTrendCN == "" {
				originalTrendCN = mtfLogic.MajorTrend
			}
			currentTrendCN := trendNameMap[currentMajorTrend]
			if currentTrendCN == "" {
				currentTrendCN = currentMajorTrend
			}
			
			// 验证side参数有效性
			if side != "long" && side != "short" {
				log.Printf("⚠️  逻辑检查警告：持仓方向无效 '%s'，使用保守策略判断", side)
			}
			
			// 检查趋势变化是否与持仓方向相反
			// 核心原则：只有当当前趋势与持仓方向明确相反时，才判定失效
			// 具体规则：
			// 1. 做多持仓：当前趋势为short时失效（无论原始趋势是什么）
			// 2. 做空持仓：当前趋势为long时失效（无论原始趋势是什么）
			// 3. neutral→long对做多持仓是好信号，neutral→short对做空持仓是好信号，不应该失效
			// 4. long→neutral或short→neutral：趋势减弱但不完全反转，暂时不判定失效
			
			if side == "long" {
				// 做多持仓：当前趋势为short时失效
				// 包括：long→short, neutral→short
				if currentMajorTrend == "short" {
					invalidReasons = append(invalidReasons, fmt.Sprintf("大周期趋势已改变：从%s变为%s（与做多持仓方向相反）", originalTrendCN, currentTrendCN))
				}
				// 注意：neutral→long对做多持仓是好信号，不失效
				// long→neutral趋势减弱但不完全反转，暂时不失效（可以继续观察）
			} else if side == "short" {
				// 做空持仓：当前趋势为long时失效
				// 包括：short→long, neutral→long
				if currentMajorTrend == "long" {
					invalidReasons = append(invalidReasons, fmt.Sprintf("大周期趋势已改变：从%s变为%s（与做空持仓方向相反）", originalTrendCN, currentTrendCN))
				}
				// 注意：neutral→short对做空持仓是好信号，不失效
				// short→neutral趋势减弱但不完全反转，暂时不失效（可以继续观察）
			} else {
				// 如果side未知，使用保守策略：
				// 只有明确趋势反转（long↔short）时才判定失效
				// neutral的变化不判定失效（因为可能是新趋势形成，也可能是趋势不明）
				if mtfLogic.MajorTrend != "neutral" && currentMajorTrend != "neutral" && mtfLogic.MajorTrend != currentMajorTrend {
					// 从long变为short或从short变为long，明确反转，判定失效
					invalidReasons = append(invalidReasons, fmt.Sprintf("大周期趋势已改变：从%s变为%s", originalTrendCN, currentTrendCN))
				}
			}
		}
	}
	
	return invalidReasons
}
