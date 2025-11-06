package storage

import (
	"backend/pkg/decision"
	"sync"
)

// PositionLogicWrapper 包装新的存储系统，提供与旧接口兼容的API
type PositionLogicWrapper struct {
	storage *PositionLogicStorage
	cache   map[string]*decision.PositionLogic
	mu      sync.RWMutex
}

// NewPositionLogicWrapper 创建持仓逻辑包装器
func NewPositionLogicWrapper(storage *PositionLogicStorage) *PositionLogicWrapper {
	wrapper := &PositionLogicWrapper{
		storage: storage,
		cache:   make(map[string]*decision.PositionLogic),
	}

	// 加载所有逻辑到缓存
	wrapper.loadAllLogics()

	return wrapper
}

// SaveEntryLogic 保存进场逻辑（兼容旧接口）
func (w *PositionLogicWrapper) SaveEntryLogic(symbol, side string, entryLogic *decision.EntryLogic) error {
	// 转换为新的EntryLogic格式
	newEntryLogic := &EntryLogic{
		Reasoning:      entryLogic.Reasoning,
		Conditions:     convertLogicConditions(entryLogic.Conditions),
		MultiTimeframe: convertMultiTimeframeLogic(entryLogic.MultiTimeframe),
		Timestamp:      entryLogic.Timestamp,
	}

	err := w.storage.SaveEntryLogic(symbol, side, newEntryLogic)
	if err != nil {
		return err
	}

	// 更新缓存
	w.mu.Lock()
	defer w.mu.Unlock()

	posKey := symbol + "_" + side
	logic, exists := w.cache[posKey]
	if !exists {
		logic = &decision.PositionLogic{}
		w.cache[posKey] = logic
	}
	logic.EntryLogic = entryLogic

	return nil
}

// SaveExitLogic 保存出场逻辑（兼容旧接口）
func (w *PositionLogicWrapper) SaveExitLogic(symbol, side string, exitLogic *decision.ExitLogic) error {
	// 转换为新的ExitLogic格式
	newExitLogic := &ExitLogic{
		Reasoning:      exitLogic.Reasoning,
		Conditions:     convertLogicConditions(exitLogic.Conditions),
		MultiTimeframe: convertMultiTimeframeLogic(exitLogic.MultiTimeframe),
		Timestamp:      exitLogic.Timestamp,
	}

	err := w.storage.SaveExitLogic(symbol, side, newExitLogic)
	if err != nil {
		return err
	}

	// 更新缓存
	w.mu.Lock()
	defer w.mu.Unlock()

	posKey := symbol + "_" + side
	logic, exists := w.cache[posKey]
	if !exists {
		logic = &decision.PositionLogic{}
		w.cache[posKey] = logic
	}
	logic.ExitLogic = exitLogic

	return nil
}

// GetLogic 获取持仓逻辑（兼容旧接口）
// 注意：为了确保读取到最新的止损止盈数据，每次都会从数据库重新加载并更新缓存
func (w *PositionLogicWrapper) GetLogic(symbol, side string) *decision.PositionLogic {
	posKey := symbol + "_" + side
	
	// 始终从数据库加载最新数据（确保读取到最新的止损止盈设置）
	dbLogic, err := w.storage.GetLogic(symbol, side)
	if err != nil {
		// 如果数据库查询失败，尝试从缓存读取（降级处理）
		w.mu.RLock()
		logic, exists := w.cache[posKey]
		w.mu.RUnlock()
		if exists {
			return logic
		}
		return nil
	}

	if dbLogic == nil {
		// 数据库中没有记录，尝试从缓存读取
		w.mu.RLock()
		logic, exists := w.cache[posKey]
		w.mu.RUnlock()
		if exists {
			return logic
		}
		return nil
	}

	// 转换为旧格式
	logic := &decision.PositionLogic{
		StopLoss:   dbLogic.StopLoss,
		TakeProfit: dbLogic.TakeProfit,
	}

	if dbLogic.EntryLogic != nil {
		logic.EntryLogic = &decision.EntryLogic{
			Reasoning:      dbLogic.EntryLogic.Reasoning,
			Conditions:     convertLogicConditionsFromNew(dbLogic.EntryLogic.Conditions),
			MultiTimeframe: convertMultiTimeframeLogicFromNew(dbLogic.EntryLogic.MultiTimeframe),
			Timestamp:      dbLogic.EntryLogic.Timestamp,
		}
	}

	if dbLogic.ExitLogic != nil {
		logic.ExitLogic = &decision.ExitLogic{
			Reasoning:      dbLogic.ExitLogic.Reasoning,
			Conditions:     convertLogicConditionsFromNew(dbLogic.ExitLogic.Conditions),
			MultiTimeframe: convertMultiTimeframeLogicFromNew(dbLogic.ExitLogic.MultiTimeframe),
			Timestamp:      dbLogic.ExitLogic.Timestamp,
		}
	}

	// 更新缓存（确保缓存与数据库同步）
	w.mu.Lock()
	w.cache[posKey] = logic
	w.mu.Unlock()

	return logic
}

// SaveStopLoss 保存止损价格（兼容旧接口）
func (w *PositionLogicWrapper) SaveStopLoss(symbol, side string, stopLoss float64) error {
	err := w.storage.SaveStopLoss(symbol, side, stopLoss)
	if err != nil {
		return err
	}

	// 更新缓存
	w.mu.Lock()
	defer w.mu.Unlock()

	posKey := symbol + "_" + side
	logic, exists := w.cache[posKey]
	if !exists {
		logic = &decision.PositionLogic{}
		w.cache[posKey] = logic
	}
	logic.StopLoss = stopLoss

	return nil
}

// SaveTakeProfit 保存止盈价格（兼容旧接口）
func (w *PositionLogicWrapper) SaveTakeProfit(symbol, side string, takeProfit float64) error {
	err := w.storage.SaveTakeProfit(symbol, side, takeProfit)
	if err != nil {
		return err
	}

	// 更新缓存
	w.mu.Lock()
	defer w.mu.Unlock()

	posKey := symbol + "_" + side
	logic, exists := w.cache[posKey]
	if !exists {
		logic = &decision.PositionLogic{}
		w.cache[posKey] = logic
	}
	logic.TakeProfit = takeProfit

	return nil
}

// SaveStopLossAndTakeProfit 同时保存止损和止盈价格（兼容旧接口）
func (w *PositionLogicWrapper) SaveStopLossAndTakeProfit(symbol, side string, stopLoss, takeProfit float64) error {
	// 先保存到数据库
	err := w.storage.SaveStopLossAndTakeProfit(symbol, side, stopLoss, takeProfit)
	if err != nil {
		return err
	}

	// 保存后，从数据库重新加载最新数据并更新缓存（确保缓存与数据库同步）
	// 这样可以确保即使只更新一个字段，另一个字段也能从数据库读取到最新值
	dbLogic, err := w.storage.GetLogic(symbol, side)
	if err == nil && dbLogic != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		
		posKey := symbol + "_" + side
		logic, exists := w.cache[posKey]
		if !exists {
			logic = &decision.PositionLogic{}
			w.cache[posKey] = logic
		}
		
		// 从数据库加载的值更新缓存（确保完整同步）
		logic.StopLoss = dbLogic.StopLoss
		logic.TakeProfit = dbLogic.TakeProfit
		
		// 更新逻辑字段（如果数据库中有）
		if dbLogic.EntryLogic != nil {
			logic.EntryLogic = &decision.EntryLogic{
				Reasoning:      dbLogic.EntryLogic.Reasoning,
				Conditions:     convertLogicConditionsFromNew(dbLogic.EntryLogic.Conditions),
				MultiTimeframe: convertMultiTimeframeLogicFromNew(dbLogic.EntryLogic.MultiTimeframe),
				Timestamp:      dbLogic.EntryLogic.Timestamp,
			}
		}
		
		if dbLogic.ExitLogic != nil {
			logic.ExitLogic = &decision.ExitLogic{
				Reasoning:      dbLogic.ExitLogic.Reasoning,
				Conditions:     convertLogicConditionsFromNew(dbLogic.ExitLogic.Conditions),
				MultiTimeframe: convertMultiTimeframeLogicFromNew(dbLogic.ExitLogic.MultiTimeframe),
				Timestamp:      dbLogic.ExitLogic.Timestamp,
			}
		}
	}

	return nil
}

// DeleteLogic 删除持仓逻辑（兼容旧接口）
func (w *PositionLogicWrapper) DeleteLogic(symbol, side string) error {
	err := w.storage.DeleteLogic(symbol, side)
	if err != nil {
		return err
	}

	// 从缓存删除
	w.mu.Lock()
	defer w.mu.Unlock()

	posKey := symbol + "_" + side
	delete(w.cache, posKey)

	return nil
}

// SaveFirstSeenTime 保存持仓首次出现时间
func (w *PositionLogicWrapper) SaveFirstSeenTime(symbol, side string, firstSeenTime int64) error {
	err := w.storage.SaveFirstSeenTime(symbol, side, firstSeenTime)
	if err != nil {
		return err
	}

	// 更新缓存
	w.mu.Lock()
	defer w.mu.Unlock()

	posKey := symbol + "_" + side
	logic, exists := w.cache[posKey]
	if !exists {
		logic = &decision.PositionLogic{}
		w.cache[posKey] = logic
	}
	// 注意：decision.PositionLogic 没有 FirstSeenTime 字段，但数据库已保存

	return nil
}

// GetFirstSeenTime 获取持仓首次出现时间
func (w *PositionLogicWrapper) GetFirstSeenTime(symbol, side string) (int64, bool) {
	// 从数据库加载
	dbLogic, err := w.storage.GetLogic(symbol, side)
	if err != nil || dbLogic == nil {
		return 0, false
	}

	if dbLogic.FirstSeenTime > 0 {
		return dbLogic.FirstSeenTime, true
	}

	return 0, false
}

// loadAllLogics 加载所有逻辑到缓存
func (w *PositionLogicWrapper) loadAllLogics() {
	// 注意：由于新的存储系统没有提供批量加载方法，这里暂时不实现
	// 实际使用时会在GetLogic时从数据库加载
}

// 转换函数
func convertLogicConditions(conditions []decision.LogicCondition) []LogicCondition {
	result := make([]LogicCondition, len(conditions))
	for i, c := range conditions {
		result[i] = LogicCondition{
			Type:        c.Type,
			Description: c.Description,
			Timeframe:   c.Timeframe,
			Value:       c.Value,
			Operator:    c.Operator,
		}
	}
	return result
}

func convertMultiTimeframeLogic(mtf *decision.MultiTimeframeLogic) *MultiTimeframeLogic {
	if mtf == nil {
		return nil
	}
	return &MultiTimeframeLogic{
		MajorTrend:    mtf.MajorTrend,
		PullbackEntry: mtf.PullbackEntry,
		Timeframes:    mtf.Timeframes,
	}
}

// 反向转换函数
func convertLogicConditionsFromNew(conditions []LogicCondition) []decision.LogicCondition {
	result := make([]decision.LogicCondition, len(conditions))
	for i, c := range conditions {
		result[i] = decision.LogicCondition{
			Type:        c.Type,
			Description: c.Description,
			Timeframe:   c.Timeframe,
			Value:       c.Value,
			Operator:    c.Operator,
		}
	}
	return result
}

func convertMultiTimeframeLogicFromNew(mtf *MultiTimeframeLogic) *decision.MultiTimeframeLogic {
	if mtf == nil {
		return nil
	}
	return &decision.MultiTimeframeLogic{
		MajorTrend:    mtf.MajorTrend,
		PullbackEntry: mtf.PullbackEntry,
		Timeframes:    mtf.Timeframes,
	}
}

