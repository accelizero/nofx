package decision

import (
	"fmt"
	"log"
	"math"
	"backend/pkg/config"
	"backend/pkg/market"
	"sort"
	"sync"
	"time"
)

// MultiTimeframeAnalyzer 多时间框架分析器（重构版本 - 逻辑正确）
type MultiTimeframeAnalyzer struct {
	config *config.MultiTimeframeConfig
	cache  *TimeframeDataCache
}

// NewMultiTimeframeAnalyzer 创建多时间框架分析器
func NewMultiTimeframeAnalyzer(mtConfig *config.MultiTimeframeConfig) *MultiTimeframeAnalyzer {
	analyzer := &MultiTimeframeAnalyzer{
		config: mtConfig,
	}
	
	if mtConfig.EnableCache {
		analyzer.cache = NewTimeframeDataCache(&mtConfig.CacheTTL)
	}
	
	return analyzer
}

// UnifiedTimeframeData 统一的时间框架数据
type UnifiedTimeframeData struct {
	Symbol       string
	DailyData    *market.Data // 日线数据
	Hourly4Data  *market.Data // 4小时数据
	Hourly1Data  *market.Data // 1小时数据
	Minute15Data *market.Data // 15分钟数据
	Minute3Data  *market.Data // 3分钟数据
}

// SymbolScore 币种评分（支持多空双向）
type SymbolScore struct {
	Symbol string
	
	// 做多评分详情
	LongScore ScoreDetails
	
	// 做空评分详情
	ShortScore ScoreDetails
	
	// 推荐方向 ("long", "short", "neutral")
	RecommendedDirection string
	
	// 总体评分（推荐方向的评分）
	TotalScore float64
	
	// 一致性评分（多维度）
	ConsistencyScore float64
}

// ScoreDetails 评分详情
type ScoreDetails struct {
	// 各时间框架评分
	DailyScore    float64
	Hourly4Score  float64
	Hourly1Score  float64
	Minute15Score float64
	Minute3Score  float64
	
	// 加权总分
	WeightedScore float64
}

// MultiTimeframeAnalysisResult 分析结果
type MultiTimeframeAnalysisResult struct {
	SymbolScores  map[string]*SymbolScore
	SortedSymbols []string
	DataMap       map[string]*UnifiedTimeframeData
}

// Analyze 分析多时间框架数据
func (mta *MultiTimeframeAnalyzer) Analyze(ctx *Context) (*MultiTimeframeAnalysisResult, error) {
	// 1. 收集需要分析的币种
	symbolSet := mta.collectSymbols(ctx)
	if len(symbolSet) == 0 {
		return &MultiTimeframeAnalysisResult{
			SymbolScores:  make(map[string]*SymbolScore),
			SortedSymbols: []string{},
			DataMap:       make(map[string]*UnifiedTimeframeData),
		}, nil
	}
	
	log.Printf("📊 多时间框架分析：开始分析 %d 个币种", len(symbolSet))
	
	// 2. 统一获取所有时间框架数据（避免重复）
	dataMap := mta.fetchAllTimeframesUnified(symbolSet)
	
	// 3. 计算每个币种的评分（支持多空双向）
	scores := mta.calculateDirectionalScores(dataMap)
	
	// 4. 按最高评分排序币种
	sortedSymbols := mta.sortSymbolsByScore(scores)
	
	log.Printf("📊 多时间框架分析完成：成功分析 %d 个币种", len(scores))
	
	return &MultiTimeframeAnalysisResult{
		SymbolScores:  scores,
		SortedSymbols: sortedSymbols,
		DataMap:       dataMap,
	}, nil
}

// collectSymbols 收集需要分析的币种
func (mta *MultiTimeframeAnalyzer) collectSymbols(ctx *Context) map[string]bool {
	symbolSet := make(map[string]bool)
	
	// 1. 优先分析持仓币种
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}
	
	// 2. 分析候选币种（只分析已通过流动性检查的）
	for _, coin := range ctx.CandidateCoins {
		if _, hasData := ctx.MarketDataMap[coin.Symbol]; hasData {
			symbolSet[coin.Symbol] = true
		}
	}
	
	return symbolSet
}

// fetchAllTimeframesUnified 统一获取所有时间框架数据（避免重复）
func (mta *MultiTimeframeAnalyzer) fetchAllTimeframesUnified(symbolSet map[string]bool) map[string]*UnifiedTimeframeData {
	dataMap := make(map[string]*UnifiedTimeframeData)
	
	var mu sync.Mutex
	var wg sync.WaitGroup
	
	// 并发获取每个币种的数据
	for symbol := range symbolSet {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			
			data := &UnifiedTimeframeData{Symbol: s}
			
			// 并发获取5个时间框架
			type result struct {
				name string
				data *market.Data
				err  error
			}
			
			results := make(chan result, 5)
			
			// 并发获取
			go func() {
				data, err := mta.fetchTimeframeData(s, "1d", 1000) // 日线：1000根，确保指标成熟
				results <- result{"1d", data, err}
			}()
			go func() {
				data, err := mta.fetchTimeframeData(s, "4h", 1000) // 4小时：1000根，确保指标成熟
				results <- result{"4h", data, err}
			}()
			go func() {
				data, err := mta.fetchTimeframeData(s, "1h", 1000) // 1小时：1000根，确保指标成熟
				results <- result{"1h", data, err}
			}()
			go func() {
				data, err := mta.fetchTimeframeData(s, "15m", 1000) // 15分钟：1000根，确保指标成熟
				results <- result{"15m", data, err}
			}()
			go func() {
				data, err := mta.fetchTimeframeData(s, "3m", 1000) // 3分钟：1000根，确保指标成熟
				results <- result{"3m", data, err}
			}()
			
			// 收集结果
			for i := 0; i < 5; i++ {
				r := <-results
				if r.err != nil {
					log.Printf("⚠️  %s %s 数据获取失败: %v", s, r.name, r.err)
					continue
				}
				if r.data == nil {
					continue
				}
				
				switch r.name {
				case "1d":
					data.DailyData = r.data
				case "4h":
					data.Hourly4Data = r.data
				case "1h":
					data.Hourly1Data = r.data
				case "15m":
					data.Minute15Data = r.data
				case "3m":
					data.Minute3Data = r.data
				}
			}
			
			// 验证至少有一个时间框架的数据
			if data.DailyData == nil && data.Hourly4Data == nil && 
			   data.Hourly1Data == nil && data.Minute15Data == nil && data.Minute3Data == nil {
				log.Printf("⚠️  %s 所有时间框架数据获取失败，跳过", s)
				return
			}
			
			// 线程安全地写入
			mu.Lock()
			dataMap[s] = data
			mu.Unlock()
		}(symbol)
	}
	
	wg.Wait()
	return dataMap
}

// fetchTimeframeData 获取指定时间框架的数据（支持缓存）
func (mta *MultiTimeframeAnalyzer) fetchTimeframeData(symbol, timeframe string, limit int) (*market.Data, error) {
	if mta.cache != nil {
		if cached := mta.cache.Get(symbol, timeframe); cached != nil {
			return cached, nil
		}
	}
	
	data, err := market.GetWithTimeframe(symbol, timeframe, limit)
	if err != nil {
		return nil, err
	}
	
	if mta.cache != nil && data != nil {
		mta.cache.Set(symbol, timeframe, data)
	}
	
	return data, nil
}

// calculateDirectionalScores 计算多空双向评分
func (mta *MultiTimeframeAnalyzer) calculateDirectionalScores(dataMap map[string]*UnifiedTimeframeData) map[string]*SymbolScore {
	scores := make(map[string]*SymbolScore)
	
	for symbol, data := range dataMap {
		score := &SymbolScore{Symbol: symbol}
		
		// 分别计算做多和做空评分
		score.LongScore = mta.calculateScoreForDirection(data, "long")
		score.ShortScore = mta.calculateScoreForDirection(data, "short")
		
		// 如果启用了回调入场策略，计算回调入场加分
		// 默认启用：如果BonusScore>0，说明配置存在，则检查Enable；如果BonusScore=0，默认启用
		shouldEnable := (mta.config.PullbackEntry.BonusScore > 0 && mta.config.PullbackEntry.Enable) || 
		                (mta.config.PullbackEntry.BonusScore == 0) // 未配置时默认启用
		
		if shouldEnable {
			// 检测"顺大逆小"信号并添加加分
			longBonus := mta.calculatePullbackEntryBonus(data, "long")
			shortBonus := mta.calculatePullbackEntryBonus(data, "short")
			
			score.LongScore.WeightedScore += longBonus
			score.ShortScore.WeightedScore += shortBonus
			
			// 限制评分在0-1范围内
			if score.LongScore.WeightedScore > 1.0 {
				score.LongScore.WeightedScore = 1.0
			}
			if score.ShortScore.WeightedScore > 1.0 {
				score.ShortScore.WeightedScore = 1.0
			}
		}
		
		// 选择推荐方向（选择评分更高的）
		if score.LongScore.WeightedScore > score.ShortScore.WeightedScore {
			score.RecommendedDirection = "long"
			score.TotalScore = score.LongScore.WeightedScore
		} else if score.ShortScore.WeightedScore > score.LongScore.WeightedScore {
			score.RecommendedDirection = "short"
			score.TotalScore = score.ShortScore.WeightedScore
		} else {
			score.RecommendedDirection = "neutral"
			score.TotalScore = (score.LongScore.WeightedScore + score.ShortScore.WeightedScore) / 2.0
		}
		
		// 计算多维度一致性
		score.ConsistencyScore = mta.calculateMultiDimensionalConsistency(data)
		
		scores[symbol] = score
	}
	
	return scores
}

// calculateScoreForDirection 计算指定方向的评分
func (mta *MultiTimeframeAnalyzer) calculateScoreForDirection(data *UnifiedTimeframeData, direction string) ScoreDetails {
	detail := ScoreDetails{}
	
	// 权重配置
	weights := mta.config.Weights
	
	// 计算各时间框架评分
	if data.DailyData != nil {
		detail.DailyScore = mta.calculateSingleTimeframeScore(data.DailyData, direction)
	} else {
		detail.DailyScore = 0.5
	}
	
	if data.Hourly4Data != nil {
		detail.Hourly4Score = mta.calculateSingleTimeframeScore(data.Hourly4Data, direction)
	} else {
		detail.Hourly4Score = 0.5
	}
	
	if data.Hourly1Data != nil {
		detail.Hourly1Score = mta.calculateSingleTimeframeScore(data.Hourly1Data, direction)
	} else {
		detail.Hourly1Score = 0.5
	}
	
	if data.Minute15Data != nil {
		detail.Minute15Score = mta.calculateSingleTimeframeScore(data.Minute15Data, direction)
	} else {
		detail.Minute15Score = 0.5
	}
	
	if data.Minute3Data != nil {
		detail.Minute3Score = mta.calculateSingleTimeframeScore(data.Minute3Data, direction)
	} else {
		detail.Minute3Score = 0.5
	}
	
	// 加权平均
	detail.WeightedScore = detail.DailyScore*weights.Daily +
		detail.Hourly4Score*weights.Hourly4 +
		detail.Hourly1Score*weights.Hourly1 +
		detail.Minute15Score*weights.Minute15 +
		detail.Minute3Score*weights.Minute3
	
	return detail
}

// calculateSingleTimeframeScore 计算单个时间框架的评分（支持多空方向）
func (mta *MultiTimeframeAnalyzer) calculateSingleTimeframeScore(data *market.Data, direction string) float64 {
	if data == nil {
		return 0.5
	}
	
	var score float64
	var count int
	
	// 1. 价格与EMA关系（根据方向调整评分逻辑）
	if data.CurrentEMA20 > 0 && data.CurrentPrice > 0 {
		emaRatio := (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20
		
		if direction == "long" {
			// 做多：价格高于EMA是好事
			if emaRatio > 0.02 {
				score += 0.8 // 价格远高于EMA，强烈看涨
			} else if emaRatio > 0 {
				score += 0.6 // 价格高于EMA，看涨
			} else if emaRatio < -0.02 {
				score += 0.2 // 价格远低于EMA，看跌（做多不利）
			} else {
				score += 0.4 // 价格低于EMA，看跌（做多不利）
			}
		} else {
			// 做空：价格低于EMA是好事
			if emaRatio < -0.02 {
				score += 0.8 // 价格远低于EMA，强烈看跌（做空有利）
			} else if emaRatio < 0 {
				score += 0.6 // 价格低于EMA，看跌（做空有利）
			} else if emaRatio > 0.02 {
				score += 0.2 // 价格远高于EMA，看涨（做空不利）
			} else {
				score += 0.4 // 价格高于EMA，看涨（做空不利）
			}
		}
		count++
	}
	
	// 2. MACD趋势
	if data.CurrentMACD != 0 {
		if direction == "long" {
			if data.CurrentMACD > 0 {
				score += 0.7 // 正MACD对做多有利
			} else {
				score += 0.3 // 负MACD对做多不利
			}
		} else {
			if data.CurrentMACD < 0 {
				score += 0.7 // 负MACD对做空有利
			} else {
				score += 0.3 // 正MACD对做空不利
			}
		}
		count++
	}
	
	// 3. RSI位置（根据方向调整）
	if data.CurrentRSI7 > 0 {
		if direction == "long" {
			// 做多：RSI超卖（<30）可能反弹，但也要谨慎
			if data.CurrentRSI7 > 30 && data.CurrentRSI7 < 70 {
				score += 0.8 // 健康区间
			} else if data.CurrentRSI7 <= 30 {
				score += 0.5 // 超卖可能反弹，但风险高
			} else {
				score += 0.2 // 超买，做多不利
			}
		} else {
			// 做空：RSI超买（>70）可能回调
			if data.CurrentRSI7 > 30 && data.CurrentRSI7 < 70 {
				score += 0.8 // 健康区间
			} else if data.CurrentRSI7 >= 70 {
				score += 0.5 // 超买可能回调，但风险高
			} else {
				score += 0.2 // 超卖，做空不利
			}
		}
		count++
	}
	
	if count == 0 {
		return 0.5
	}
	
	score = score / float64(count)
	
	// 限制在0-1范围内
	if score < 0 {
		score = 0
	} else if score > 1 {
		score = 1
	}
	
	return score
}

// calculateMultiDimensionalConsistency 计算多维度一致性
func (mta *MultiTimeframeAnalyzer) calculateMultiDimensionalConsistency(data *UnifiedTimeframeData) float64 {
	// 收集所有时间框架的数据
	timeframes := []*market.Data{}
	if data.DailyData != nil {
		timeframes = append(timeframes, data.DailyData)
	}
	if data.Hourly4Data != nil {
		timeframes = append(timeframes, data.Hourly4Data)
	}
	if data.Hourly1Data != nil {
		timeframes = append(timeframes, data.Hourly1Data)
	}
	if data.Minute15Data != nil {
		timeframes = append(timeframes, data.Minute15Data)
	}
	if data.Minute3Data != nil {
		timeframes = append(timeframes, data.Minute3Data)
	}
	
	if len(timeframes) == 0 {
		return 0.5
	}
	
	// 1. 趋势一致性（EMA方向）
	trendConsistency := mta.calculateTrendConsistency(timeframes)
	
	// 2. 动量一致性（MACD方向）
	momentumConsistency := mta.calculateMomentumConsistency(timeframes)
	
	// 3. 波动一致性（RSI位置）
	volatilityConsistency := mta.calculateVolatilityConsistency(timeframes)
	
	// 加权平均（趋势权重更高）
	consistency := trendConsistency*0.5 + momentumConsistency*0.3 + volatilityConsistency*0.2
	
	return consistency
}

// calculateTrendConsistency 计算趋势一致性（基于EMA方向）
func (mta *MultiTimeframeAnalyzer) calculateTrendConsistency(timeframes []*market.Data) float64 {
	directions := []float64{}
	const emaTolerance = 0.001
	
	for _, tf := range timeframes {
		if tf.CurrentEMA20 > 0 {
			emaDiff := (tf.CurrentPrice - tf.CurrentEMA20) / tf.CurrentEMA20
			if emaDiff > emaTolerance {
				directions = append(directions, 1.0) // 看涨
			} else if emaDiff < -emaTolerance {
				directions = append(directions, -1.0) // 看跌
			}
			// 中性方向不参与一致性计算
		}
	}
	
	if len(directions) == 0 {
		return 0.5
	}
	
	positiveCount := 0
	negativeCount := 0
	for _, dir := range directions {
		if dir > 0 {
			positiveCount++
		} else {
			negativeCount++
		}
	}
	
	maxSameDirection := positiveCount
	if negativeCount > positiveCount {
		maxSameDirection = negativeCount
	}
	
	consistency := float64(maxSameDirection) / float64(len(directions))
	
	// 映射到0-1范围
	if consistency >= 0.75 {
		return 0.9
	} else if consistency >= 0.5 {
		return 0.7
	} else {
		return 0.3
	}
}

// calculateMomentumConsistency 计算动量一致性（基于MACD方向）
func (mta *MultiTimeframeAnalyzer) calculateMomentumConsistency(timeframes []*market.Data) float64 {
	directions := []float64{}
	
	for _, tf := range timeframes {
		if tf.CurrentMACD != 0 {
			if tf.CurrentMACD > 0 {
				directions = append(directions, 1.0)
			} else {
				directions = append(directions, -1.0)
			}
		}
	}
	
	if len(directions) == 0 {
		return 0.5
	}
	
	positiveCount := 0
	negativeCount := 0
	for _, dir := range directions {
		if dir > 0 {
			positiveCount++
		} else {
			negativeCount++
		}
	}
	
	maxSameDirection := positiveCount
	if negativeCount > positiveCount {
		maxSameDirection = negativeCount
	}
	
	consistency := float64(maxSameDirection) / float64(len(directions))
	return consistency
}

// calculateVolatilityConsistency 计算波动一致性（基于RSI位置）
func (mta *MultiTimeframeAnalyzer) calculateVolatilityConsistency(timeframes []*market.Data) float64 {
	rsiValues := []float64{}
	
	for _, tf := range timeframes {
		if tf.CurrentRSI7 > 0 {
			rsiValues = append(rsiValues, tf.CurrentRSI7)
		}
	}
	
	if len(rsiValues) == 0 {
		return 0.5
	}
	
	// 计算RSI值的标准差（越小越一致）
	var sum, mean, variance float64
	for _, rsi := range rsiValues {
		sum += rsi
	}
	mean = sum / float64(len(rsiValues))
	
	for _, rsi := range rsiValues {
		variance += math.Pow(rsi-mean, 2)
	}
	variance /= float64(len(rsiValues))
	stdDev := math.Sqrt(variance)
	
	// 标准差越小，一致性越高（映射到0-1）
	// RSI范围0-100，标准差最大约50，归一化
	consistency := 1.0 - (stdDev / 50.0)
	if consistency < 0 {
		consistency = 0
	} else if consistency > 1 {
		consistency = 1
	}
	
	return consistency
}

// sortSymbolsByScore 按评分排序币种
func (mta *MultiTimeframeAnalyzer) sortSymbolsByScore(scores map[string]*SymbolScore) []string {
	type scoredSymbol struct {
		symbol string
		score  float64
	}
	
	scoredList := make([]scoredSymbol, 0, len(scores))
	for symbol, score := range scores {
		// 结合总体评分和一致性评分
		combinedScore := score.TotalScore*0.7 + score.ConsistencyScore*0.3
		scoredList = append(scoredList, scoredSymbol{symbol: symbol, score: combinedScore})
	}
	
	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})
	
	result := make([]string, len(scoredList))
	for i, item := range scoredList {
		result[i] = item.symbol
	}
	
	return result
}

// TimeframeDataCache 时间框架数据缓存
type TimeframeDataCache struct {
	mu    sync.RWMutex
	cache map[string]*CachedTimeframeData
	ttl   *config.MultiTimeframeCacheTTL
}

// CachedTimeframeData 缓存的时间框架数据
type CachedTimeframeData struct {
	Data      *market.Data
	Timestamp time.Time
	TTL       time.Duration
}

// NewTimeframeDataCache 创建时间框架数据缓存
func NewTimeframeDataCache(ttl *config.MultiTimeframeCacheTTL) *TimeframeDataCache {
	return &TimeframeDataCache{
		cache: make(map[string]*CachedTimeframeData),
		ttl:   ttl,
	}
}

// Get 获取缓存数据
func (c *TimeframeDataCache) Get(symbol, timeframe string) *market.Data {
	key := fmt.Sprintf("%s:%s", symbol, timeframe)
	
	c.mu.RLock()
	cached, exists := c.cache[key]
	c.mu.RUnlock()
	
	if !exists {
		return nil
	}
	
	// 检查是否过期
	if time.Since(cached.Timestamp) > cached.TTL {
		c.mu.Lock()
		delete(c.cache, key)
		c.mu.Unlock()
		return nil
	}
	
	return cached.Data
}

// Set 设置缓存数据
func (c *TimeframeDataCache) Set(symbol, timeframe string, data *market.Data) {
	key := fmt.Sprintf("%s:%s", symbol, timeframe)
	
	var ttl time.Duration
	switch timeframe {
	case "1d":
		ttl = time.Duration(c.ttl.Daily) * time.Second
	case "4h":
		ttl = time.Duration(c.ttl.Hourly4) * time.Second
	case "1h":
		ttl = time.Duration(c.ttl.Hourly1) * time.Second
	case "15m":
		ttl = time.Duration(c.ttl.Minute15) * time.Second
	case "3m":
		ttl = time.Duration(c.ttl.Minute3) * time.Second
	default:
		ttl = 60 * time.Second // 默认1分钟
	}
	
	c.mu.Lock()
	c.cache[key] = &CachedTimeframeData{
		Data:      data,
		Timestamp: time.Now(),
		TTL:       ttl,
	}
	c.mu.Unlock()
}

// calculatePullbackEntryBonus 计算回调入场加分（"顺大逆小"策略）
// 返回：加分值（0 到 config.PullbackEntry.BonusScore）
func (mta *MultiTimeframeAnalyzer) calculatePullbackEntryBonus(data *UnifiedTimeframeData, direction string) float64 {
	// 1. 检测大周期趋势方向
	majorTrend, trendStrength := mta.detectMajorTrend(data)
	if majorTrend == "neutral" || trendStrength < 0.7 {
		// 大周期趋势不明确，不给予加分
		return 0
	}
	
	// 2. 检查大周期趋势是否与目标方向一致
	if (direction == "long" && majorTrend != "long") || 
	   (direction == "short" && majorTrend != "short") {
		// 大周期趋势与目标方向不一致，不给予加分
		return 0
	}
	
	// 3. 检测小周期是否回调
	pullbackDetected, pullbackStrength := mta.detectSmallTimeframePullback(data, majorTrend)
	if !pullbackDetected || pullbackStrength < 0.3 {
		// 小周期没有回调或回调不明显，不给予加分
		return 0
	}
	
	// 4. 检测小周期反转信号
	reversalDetected, reversalStrength := mta.detectReversalSignal(data, majorTrend)
	if !reversalDetected || reversalStrength < 0.4 {
		// 反转信号不明确，不给予加分
		return 0
	}
	
	// 5. 计算综合加分
	// 综合考虑：趋势强度 + 回调强度 + 反转强度
	combinedStrength := (trendStrength*0.4 + pullbackStrength*0.3 + reversalStrength*0.3)
	bonusScore := mta.config.PullbackEntry.BonusScore
	if bonusScore == 0 {
		bonusScore = 0.15 // 默认加分0.15（如果未配置）
	}
	bonus := bonusScore * combinedStrength
	
	return bonus
}

// detectMajorTrend 检测大周期趋势方向（日线 + 4小时）
// 返回：方向（"long"/"short"/"neutral"）+ 趋势强度（0-1）
func (mta *MultiTimeframeAnalyzer) detectMajorTrend(data *UnifiedTimeframeData) (string, float64) {
	var bullishCount, bearishCount int
	var totalStrength float64
	
	// 检查日线
	if data.DailyData != nil && data.DailyData.CurrentEMA20 > 0 && data.DailyData.CurrentPrice > 0 {
		priceAboveEMA := data.DailyData.CurrentPrice > data.DailyData.CurrentEMA20
		macdPositive := data.DailyData.CurrentMACD > 0
		
		if priceAboveEMA && macdPositive {
			bullishCount++
			totalStrength += 0.5
		} else if !priceAboveEMA && !macdPositive {
			bearishCount++
			totalStrength += 0.5
		}
	}
	
	// 检查4小时
	if data.Hourly4Data != nil && data.Hourly4Data.CurrentEMA20 > 0 && data.Hourly4Data.CurrentPrice > 0 {
		priceAboveEMA := data.Hourly4Data.CurrentPrice > data.Hourly4Data.CurrentEMA20
		macdPositive := data.Hourly4Data.CurrentMACD > 0
		
		if priceAboveEMA && macdPositive {
			bullishCount++
			totalStrength += 0.5
		} else if !priceAboveEMA && !macdPositive {
			bearishCount++
			totalStrength += 0.5
		}
	}
	
	// 判断趋势方向
	if bullishCount > bearishCount && bullishCount >= 1 {
		strength := totalStrength / float64(bullishCount+bearishCount)
		return "long", strength
	} else if bearishCount > bullishCount && bearishCount >= 1 {
		strength := totalStrength / float64(bullishCount+bearishCount)
		return "short", strength
	}
	
	return "neutral", 0
}

// detectSmallTimeframePullback 检测小周期是否回调（1小时 + 15分钟）
// 返回：是否回调 + 回调强度（0-1）
func (mta *MultiTimeframeAnalyzer) detectSmallTimeframePullback(data *UnifiedTimeframeData, majorTrend string) (bool, float64) {
	var pullbackCount int
	var totalStrength float64
	
	// 检查1小时
	if data.Hourly1Data != nil && data.Hourly1Data.CurrentEMA20 > 0 && data.Hourly1Data.CurrentPrice > 0 {
		priceAboveEMA := data.Hourly1Data.CurrentPrice > data.Hourly1Data.CurrentEMA20
		macdPositive := data.Hourly1Data.CurrentMACD > 0
		
		// 如果大周期看涨，但1小时回调（价格<EMA或MACD<0）
		if majorTrend == "long" {
			if !priceAboveEMA || !macdPositive {
				pullbackCount++
				// 计算回调强度：价格偏离EMA越多，回调越明显
				emaRatio := (data.Hourly1Data.CurrentPrice - data.Hourly1Data.CurrentEMA20) / data.Hourly1Data.CurrentEMA20
				if emaRatio < -0.01 {
					totalStrength += 0.5 // 明显回调
				} else {
					totalStrength += 0.3 // 轻微回调
				}
			}
		} else if majorTrend == "short" {
			// 如果大周期看跌，但1小时反弹（价格>EMA或MACD>0）
			if priceAboveEMA || macdPositive {
				pullbackCount++
				emaRatio := (data.Hourly1Data.CurrentPrice - data.Hourly1Data.CurrentEMA20) / data.Hourly1Data.CurrentEMA20
				if emaRatio > 0.01 {
					totalStrength += 0.5 // 明显反弹
				} else {
					totalStrength += 0.3 // 轻微反弹
				}
			}
		}
	}
	
	// 检查15分钟
	if data.Minute15Data != nil && data.Minute15Data.CurrentEMA20 > 0 && data.Minute15Data.CurrentPrice > 0 {
		priceAboveEMA := data.Minute15Data.CurrentPrice > data.Minute15Data.CurrentEMA20
		macdPositive := data.Minute15Data.CurrentMACD > 0
		
		if majorTrend == "long" {
			if !priceAboveEMA || !macdPositive {
				pullbackCount++
				emaRatio := (data.Minute15Data.CurrentPrice - data.Minute15Data.CurrentEMA20) / data.Minute15Data.CurrentEMA20
				if emaRatio < -0.01 {
					totalStrength += 0.5
				} else {
					totalStrength += 0.3
				}
			}
		} else if majorTrend == "short" {
			if priceAboveEMA || macdPositive {
				pullbackCount++
				emaRatio := (data.Minute15Data.CurrentPrice - data.Minute15Data.CurrentEMA20) / data.Minute15Data.CurrentEMA20
				if emaRatio > 0.01 {
					totalStrength += 0.5
				} else {
					totalStrength += 0.3
				}
			}
		}
	}
	
	if pullbackCount == 0 {
		return false, 0
	}
	
	strength := totalStrength / float64(pullbackCount)
	return true, strength
}

// detectReversalSignal 检测小周期反转信号（从回调状态转回大周期方向）
// 返回：是否反转 + 反转强度（0-1）
func (mta *MultiTimeframeAnalyzer) detectReversalSignal(data *UnifiedTimeframeData, majorTrend string) (bool, float64) {
	var signalCount int
	var totalStrength float64
	
	// 检查1小时反转信号
	if data.Hourly1Data != nil {
		signalDetected, strength := mta.checkReversalSignalForTimeframe(data.Hourly1Data, majorTrend)
		if signalDetected {
			signalCount++
			totalStrength += strength
		}
	}
	
	// 检查15分钟反转信号
	if data.Minute15Data != nil {
		signalDetected, strength := mta.checkReversalSignalForTimeframe(data.Minute15Data, majorTrend)
		if signalDetected {
			signalCount++
			totalStrength += strength
		}
	}
	
	if signalCount == 0 {
		return false, 0
	}
	
	strength := totalStrength / float64(signalCount)
	return true, strength
}

// checkReversalSignalForTimeframe 检查单个时间框架的反转信号
func (mta *MultiTimeframeAnalyzer) checkReversalSignalForTimeframe(data *market.Data, majorTrend string) (bool, float64) {
	if data == nil || data.CurrentEMA20 <= 0 || data.CurrentPrice <= 0 {
		return false, 0
	}
	
	var signalCount int
	var totalStrength float64
	
	if majorTrend == "long" {
		// 做多反转信号：从回调状态转回上涨
		// 1. MACD从负转正（或接近转正）
		if data.CurrentMACD > -0.0001 && data.CurrentMACD < 0.0001 {
			// MACD接近0，可能即将转正
			signalCount++
			totalStrength += 0.3
		} else if data.CurrentMACD > 0 {
			// MACD已转正
			signalCount++
			totalStrength += 0.5
		}
		
		// 2. RSI从超卖反弹（<30 → 30-50）
		if data.CurrentRSI7 > 0 {
			if data.CurrentRSI7 >= 30 && data.CurrentRSI7 < 50 {
				// RSI从超卖区域反弹
				signalCount++
				totalStrength += 0.4
			} else if data.CurrentRSI7 >= 25 && data.CurrentRSI7 < 30 {
				// RSI接近超卖，可能反弹
				signalCount++
				totalStrength += 0.2
			}
		}
		
		// 3. 价格从EMA下方回到EMA附近（或上方）
		emaRatio := (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20
		if emaRatio > -0.005 && emaRatio < 0.01 {
			// 价格接近EMA，可能反转
			signalCount++
			totalStrength += 0.3
		} else if emaRatio >= 0.01 {
			// 价格已回到EMA上方
			signalCount++
			totalStrength += 0.4
		}
	} else if majorTrend == "short" {
		// 做空反转信号：从反弹状态转回下跌
		// 1. MACD从正转负（或接近转负）
		if data.CurrentMACD > -0.0001 && data.CurrentMACD < 0.0001 {
			signalCount++
			totalStrength += 0.3
		} else if data.CurrentMACD < 0 {
			signalCount++
			totalStrength += 0.5
		}
		
		// 2. RSI从超买回落（>70 → 50-70）
		if data.CurrentRSI7 > 0 {
			if data.CurrentRSI7 <= 70 && data.CurrentRSI7 > 50 {
				signalCount++
				totalStrength += 0.4
			} else if data.CurrentRSI7 <= 75 && data.CurrentRSI7 > 70 {
				signalCount++
				totalStrength += 0.2
			}
		}
		
		// 3. 价格从EMA上方回到EMA附近（或下方）
		emaRatio := (data.CurrentPrice - data.CurrentEMA20) / data.CurrentEMA20
		if emaRatio < 0.005 && emaRatio > -0.01 {
			signalCount++
			totalStrength += 0.3
		} else if emaRatio <= -0.01 {
			signalCount++
			totalStrength += 0.4
		}
	}
	
	if signalCount == 0 {
		return false, 0
	}
	
	// 至少需要2个信号确认反转
	if signalCount >= 2 {
		strength := totalStrength / float64(signalCount)
		return true, strength
	}
	
	return false, 0
}