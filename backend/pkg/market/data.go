package market

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// 全局变量：当前使用的交易所API基础URL
var (
	currentExchange    = "aster" // 默认使用Aster
	baseAPIURL         = "https://fapi.asterdex.com"
	exchangeMutex      sync.RWMutex
)

// SetExchange 设置使用的交易所（仅支持aster）
func SetExchange(exchange string) {
	exchangeMutex.Lock()
	defer exchangeMutex.Unlock()

	currentExchange = strings.ToLower(exchange)
	
	if currentExchange == "aster" {
		// Aster 使用其自己的API端点
		baseAPIURL = "https://fapi.asterdex.com"
		log.Printf("📊 市场数据API: 已切换到Aster平台")
	} else {
		// 默认使用Aster
		currentExchange = "aster"
		baseAPIURL = "https://fapi.asterdex.com"
		log.Printf("📊 市场数据API: 未知交易所 '%s'，默认使用Aster", exchange)
	}
}

// Data 市场数据结构
type Data struct {
	Symbol            string
	CurrentPrice      float64
	PriceChange1h     float64 // 1小时价格变化百分比
	PriceChange4h     float64 // 4小时价格变化百分比
	CurrentEMA20      float64
	CurrentMACD       float64
	CurrentRSI7       float64
	OpenInterest      *OIData
	FundingRate       float64
	IntradaySeries    *IntradayData
}

// OIData Open Interest数据
type OIData struct {
	Latest  float64
	Average float64
}

// IntradayData 日内数据(3分钟间隔)
type IntradayData struct {
	MidPrices   []float64
	VolumeValues []float64 // 成交量序列
	EMA20Values []float64
	MACDValues  []float64 // MACD HIST（柱状图）= DIF - DEA
	DIFValues   []float64 // DIF序列（MACD线）= EMA12 - EMA26
	DEAValues   []float64 // DEA序列（信号线）= DIF的9期EMA
	RSI7Values  []float64
	RSI14Values []float64
}

// Kline K线数据
type Kline struct {
	OpenTime  int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	CloseTime int64
}

// GetWithTimeframe 获取指定时间框架的市场数据
func GetWithTimeframe(symbol, timeframe string, limit int) (*Data, error) {
	// 标准化symbol
	symbol = Normalize(symbol)

	// 获取指定时间框架的K线数据
	klines, err := getKlines(symbol, timeframe, limit)
	if err != nil {
		return nil, fmt.Errorf("获取%s K线失败: %v", timeframe, err)
	}

	// 安全检查：确保K线数据不为空
	if len(klines) == 0 {
		return nil, fmt.Errorf("获取%s K线成功但返回空数组", timeframe)
	}

	// 计算当前指标 (基于指定时间框架的最新数据)
	currentPrice := klines[len(klines)-1].Close
	currentEMA20 := calculateEMA(klines, 20)
	currentMACD := calculateMACD(klines)
	currentRSI7 := calculateRSI(klines, 7)
	
	// 处理NaN值：如果计算结果为NaN，使用0作为默认值（向后兼容）
	if math.IsNaN(currentEMA20) {
		currentEMA20 = 0
	}
	if math.IsNaN(currentMACD) {
		currentMACD = 0
	}
	if math.IsNaN(currentRSI7) {
		currentRSI7 = 0
	}

	// 计算价格变化百分比
	// 对于不同时间框架，计算对应的时间段变化
	priceChange1h := 0.0
	// 根据时间框架计算1小时相对应的K线数量
	klinesPerHour := 0
	switch timeframe {
	case "1m":
		klinesPerHour = 60
	case "3m":
		klinesPerHour = 20
	case "5m":
		klinesPerHour = 12
	case "15m":
		klinesPerHour = 4
	case "30m":
		klinesPerHour = 2
	case "1h":
		klinesPerHour = 1
	case "4h":
		klinesPerHour = 0 // 4小时框架无法直接计算1小时变化
	}

	if klinesPerHour > 0 && len(klines) >= klinesPerHour+1 {
		price1hAgo := klines[len(klines)-klinesPerHour-1].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	}

	// 4小时价格变化 - 根据当前时间框架计算
	priceChange4h := 0.0
	if timeframe == "4h" {
		// 如果是4h时间框架，直接计算相对于前一个4h K线的变化
		if len(klines) >= 2 {
			price4hAgo := klines[len(klines)-2].Close
			if price4hAgo > 0 {
				priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
			}
		}
	} else {
		// 对于其他时间框架，计算相当于4小时的变化
		// 根据时间框架计算4小时对应的K线数量
		klinesPer4h := 0
		switch timeframe {
		case "1m":
			klinesPer4h = 240
		case "3m":
			klinesPer4h = 80
		case "5m":
			klinesPer4h = 48
		case "15m":
			klinesPer4h = 16
		case "30m":
			klinesPer4h = 8
		case "1h":
			klinesPer4h = 4
		}
		if klinesPer4h > 0 && len(klines) >= klinesPer4h+1 {
			price4hAgo := klines[len(klines)-klinesPer4h-1].Close
			if price4hAgo > 0 {
				priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
			}
		}
	}

	// 获取OI数据
	oiData, err := getOpenInterestData(symbol)
	if err != nil {
		// OI失败不影响整体,使用默认值
		oiData = &OIData{Latest: 0, Average: 0}
		log.Printf("⚠️  获取 %s OI数据失败，使用默认值: %v", symbol, err)
	}

	// 获取Funding Rate
	fundingRate, err := getFundingRate(symbol)
	if err != nil {
		log.Printf("⚠️  获取 %s 资金费率失败: %v", symbol, err)
		fundingRate = 0
	}

	// 计算日内系列数据（根据时间框架调整）
	intradayData := calculateIntradaySeriesForTimeframe(klines, timeframe)

	return &Data{
		Symbol:         symbol,
		CurrentPrice:   currentPrice,
		PriceChange1h:  priceChange1h,
		PriceChange4h:  priceChange4h,
		CurrentEMA20:   currentEMA20,
		CurrentMACD:    currentMACD,
		CurrentRSI7:    currentRSI7,
		OpenInterest:   oiData,
		FundingRate:    fundingRate,
		IntradaySeries: intradayData,
	}, nil
}

// safeGetLastN 安全地获取序列的最后N个值
func safeGetLastN(seq []float64, n int) []float64 {
	if len(seq) == 0 {
		return []float64{}
	}
	if len(seq) <= n {
		return seq
	}
	return seq[len(seq)-n:]
}

// calculateIntradaySeriesForTimeframe 计算指定时间框架的日内系列数据
// 使用序列计算优化（O(n)时间复杂度），避免O(n^2)的重复计算
func calculateIntradaySeriesForTimeframe(klines []Kline, timeframe string) *IntradayData {
	data := &IntradayData{
		MidPrices:   make([]float64, 0, 7),
		VolumeValues: make([]float64, 0, 7),
		EMA20Values: make([]float64, 0, 7),
		MACDValues:  make([]float64, 0, 7),
		DIFValues:   make([]float64, 0, 7),
		DEAValues:   make([]float64, 0, 7),
		RSI7Values:  make([]float64, 0, 7),
		RSI14Values: make([]float64, 0, 7),
	}

	// 获取最近7个数据点的价格和成交量
	start := len(klines) - 7
	if start < 0 {
		start = 0
	}
	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.VolumeValues = append(data.VolumeValues, klines[i].Volume)
	}

	// 在循环外计算完整序列（O(n)时间复杂度）
	// 1. EMA20序列
	fullEma20Seq := calculateEMASequence(klines, 20)
	data.EMA20Values = safeGetLastN(fullEma20Seq, 7)

	// 2. MACD序列（DIF、DEA、HIST）
	fullDifSeq, fullDeaSeq, fullHistSeq := calculateMACDSequence(klines)
	data.DIFValues = safeGetLastN(fullDifSeq, 7)
	data.DEAValues = safeGetLastN(fullDeaSeq, 7)
	data.MACDValues = safeGetLastN(fullHistSeq, 7)

	// 3. RSI序列
	fullRsi7Seq := calculateRSISequence(klines, 7)
	data.RSI7Values = safeGetLastN(fullRsi7Seq, 7)
	
	fullRsi14Seq := calculateRSISequence(klines, 14)
	data.RSI14Values = safeGetLastN(fullRsi14Seq, 7)

	return data
}

// Get 获取指定代币的市场数据（默认3分钟时间框架）
func Get(symbol string) (*Data, error) {
	return GetWithTimeframe(symbol, "3m", 1000)
}

// getKlines 获取K线数据（支持多平台）
func getKlines(symbol, interval string, limit int) ([]Kline, error) {
	exchangeMutex.RLock()
	apiURL := baseAPIURL
	exchangeMutex.RUnlock()
	
	url := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=%s&limit=%d",
		apiURL, symbol, interval, limit)

	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		// 尝试解析错误响应
		var errorResp struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(body, &errorResp) == nil {
			return nil, fmt.Errorf("API错误 (状态码 %d): code=%d, msg=%s", resp.StatusCode, errorResp.Code, errorResp.Msg)
		}
		return nil, fmt.Errorf("API错误 (状态码 %d): %s", resp.StatusCode, string(body))
	}

	// 尝试解析为数组格式（正常响应）
	var rawData [][]interface{}
	if err := json.Unmarshal(body, &rawData); err != nil {
		// 如果不是数组格式，可能是错误响应（对象格式）
		var errorResp struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(body, &errorResp) == nil {
			return nil, fmt.Errorf("API错误: code=%d, msg=%s", errorResp.Code, errorResp.Msg)
		}
		// 如果既不是数组也不是已知错误格式，返回原始错误
		return nil, fmt.Errorf("JSON解析失败: %w, 响应内容: %s", err, string(body))
	}

	// 检查数组是否为空
	if len(rawData) == 0 {
		return nil, fmt.Errorf("API返回空数组（币种可能不存在）")
	}

	klines := make([]Kline, len(rawData))
	for i, item := range rawData {
		if len(item) < 7 {
			return nil, fmt.Errorf("K线数据格式错误：数组长度不足，需要至少7个元素，实际: %d", len(item))
		}

		// 安全地解析openTime（支持多种数字类型）
		openTimeVal, err := parseFloat(item[0])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：openTime解析失败 (索引%d): %v", i, err)
		}
		openTime := int64(openTimeVal)

		open, err := parseFloat(item[1])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：open解析失败 (索引%d): %v", i, err)
		}
		high, err := parseFloat(item[2])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：high解析失败 (索引%d): %v", i, err)
		}
		low, err := parseFloat(item[3])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：low解析失败 (索引%d): %v", i, err)
		}
		close, err := parseFloat(item[4])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：close解析失败 (索引%d): %v", i, err)
		}
		volume, err := parseFloat(item[5])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：volume解析失败 (索引%d): %v", i, err)
		}

		// 安全地解析closeTime（支持多种数字类型）
		closeTimeVal, err := parseFloat(item[6])
		if err != nil {
			return nil, fmt.Errorf("K线数据格式错误：closeTime解析失败 (索引%d): %v", i, err)
		}
		closeTime := int64(closeTimeVal)

		klines[i] = Kline{
			OpenTime:  openTime,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close,
			Volume:    volume,
			CloseTime: closeTime,
		}
	}

	return klines, nil
}

// calculateEMA 计算EMA
// 注意：假设K线数据按时间顺序排列（从旧到新，即klines[0]是最早的，klines[len-1]是最新的）
// API默认返回的就是这种顺序，如果数据顺序错误，计算结果会不正确
// 数据不足时返回NaN（使用math.NaN()），调用方需要检查
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return math.NaN()
	}

	// 计算SMA作为初始EMA（从数组开头开始，假设是时间最早的）
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// 计算EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateEMASequence 计算EMA序列（增量计算，O(n)时间复杂度）
// 返回每个时间点的EMA值序列
func calculateEMASequence(klines []Kline, period int) []float64 {
	if len(klines) < period {
		return nil
	}

	sequence := make([]float64, 0, len(klines)-period+1)
	multiplier := 2.0 / float64(period+1)

	// 计算初始SMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)
	sequence = append(sequence, ema)

	// 增量计算后续EMA值
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
		sequence = append(sequence, ema)
	}

	return sequence
}

// calculateEMASequenceFromValues 从值序列计算EMA序列（用于DIF序列计算DEA）
func calculateEMASequenceFromValues(values []float64, period int) []float64 {
	if len(values) < period {
		return nil
	}

	sequence := make([]float64, 0, len(values)-period+1)
	multiplier := 2.0 / float64(period+1)

	// 计算初始SMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	ema := sum / float64(period)
	sequence = append(sequence, ema)

	// 增量计算后续EMA值
	for i := period; i < len(values); i++ {
		ema = (values[i]-ema)*multiplier + ema
		sequence = append(sequence, ema)
	}

	return sequence
}

// calculateMACD 计算MACD（返回MACD柱状图，即HIST = DIF - DEA）
// 标准MACD指标包括：
// - DIF（MACD线）= EMA12 - EMA26
// - DEA（信号线）= DIF的9期EMA
// - HIST（柱状图）= DIF - DEA（这是最常用的MACD值，与Python版本的MACD_HIST一致）
// 使用优化版本计算，数据不足时返回NaN
func calculateMACD(klines []Kline) float64 {
	// MACD需要至少35根K线：
	// - 26根用于计算EMA26（DIF）
	// - 从第26根开始计算DIF序列，需要至少9根DIF值才能计算DEA
	if len(klines) < 35 {
		// 如果数据不足，尝试返回DIF（虽然不完整，但比返回NaN好）
		if len(klines) >= 26 {
			ema12 := calculateEMA(klines, 12)
			ema26 := calculateEMA(klines, 26)
			if math.IsNaN(ema12) || math.IsNaN(ema26) {
				return math.NaN()
			}
			return ema12 - ema26
		}
		return math.NaN()
	}

	// 第一步：使用增量计算EMA序列（O(n)时间复杂度）
	ema12Seq := calculateEMASequence(klines, 12)
	ema26Seq := calculateEMASequence(klines, 26)

	// 计算DIF序列（从第26根K线开始，因为EMA26需要26根K线）
	if len(ema12Seq) == 0 || len(ema26Seq) == 0 {
		return math.NaN()
	}

	// EMA12序列长度 = len(klines) - 12 + 1
	// EMA26序列长度 = len(klines) - 26 + 1
	// DIF序列应该从EMA26序列开始的位置对应
	difValues := make([]float64, 0, len(ema26Seq))
	ema12StartIdx := len(ema12Seq) - len(ema26Seq)
	
	for i := 0; i < len(ema26Seq); i++ {
		ema12Idx := ema12StartIdx + i
		if ema12Idx >= 0 && ema12Idx < len(ema12Seq) {
			difAtI := ema12Seq[ema12Idx] - ema26Seq[i]
			difValues = append(difValues, difAtI)
		}
	}

	// 如果DIF序列长度不足9，无法计算DEA
	if len(difValues) < 9 {
		// 降级：返回最后一个DIF值（如果存在）
		if len(difValues) > 0 {
			return difValues[len(difValues)-1]
		}
		return math.NaN()
	}

	// 第二步：计算信号线（DEA）= 对DIF序列计算9期EMA（使用优化版本）
	deaSeq := calculateEMASequenceFromValues(difValues, 9)
	if len(deaSeq) == 0 {
		// 如果无法计算DEA，返回最后一个DIF值
		return difValues[len(difValues)-1]
	}

	// 第三步：计算MACD柱状图（HIST）= 当前DIF - DEA
	// 使用最后一个DIF值（对应最新的K线）
	currentDif := difValues[len(difValues)-1]
	dea := deaSeq[len(deaSeq)-1]
	hist := (currentDif - dea) * 2.0 // 乘以2.0以跟随交易所规则

	return hist
}

// calculateMACDWithComponents 计算MACD并返回DIF、DEA、HIST三个组件（优化版本，O(n)时间复杂度）
// 返回值：(DIF, DEA, HIST)
// - DIF = EMA12 - EMA26
// - DEA = DIF的9期EMA
// - HIST = DIF - DEA
// 数据不足时返回NaN
func calculateMACDWithComponents(klines []Kline) (float64, float64, float64) {
	if len(klines) < 26 {
		return math.NaN(), math.NaN(), math.NaN()
	}

	// 第一步：使用增量计算EMA序列（O(n)时间复杂度）
	ema12Seq := calculateEMASequence(klines, 12)
	ema26Seq := calculateEMASequence(klines, 26)

	// 计算DIF序列（从第26根K线开始，因为EMA26需要26根K线）
	// EMA12序列从第12根开始，EMA26序列从第26根开始
	// 所以DIF序列从第26根开始（取两个序列的交集）
	if len(ema12Seq) == 0 || len(ema26Seq) == 0 {
		return 0, 0, 0
	}

	// EMA12序列长度 = len(klines) - 12 + 1
	// EMA26序列长度 = len(klines) - 26 + 1
	// DIF序列应该从EMA26序列开始的位置对应
	// 即：ema12Seq的索引从 len(klines) - len(ema26Seq) 开始
	difValues := make([]float64, 0, len(ema26Seq))
	ema12StartIdx := len(ema12Seq) - len(ema26Seq)
	
	for i := 0; i < len(ema26Seq); i++ {
		ema12Idx := ema12StartIdx + i
		if ema12Idx >= 0 && ema12Idx < len(ema12Seq) {
			difAtI := ema12Seq[ema12Idx] - ema26Seq[i]
			difValues = append(difValues, difAtI)
		}
	}

	if len(difValues) == 0 {
		return math.NaN(), math.NaN(), math.NaN()
	}

	// 获取当前DIF值
	currentDif := difValues[len(difValues)-1]

	// 如果DIF序列长度不足9，无法计算DEA
	if len(difValues) < 9 {
		// 降级：只返回DIF，DEA和HIST为NaN
		return currentDif, math.NaN(), math.NaN()
	}

	// 第二步：计算信号线（DEA）= 对DIF序列计算9期EMA（使用优化的序列计算）
	deaSeq := calculateEMASequenceFromValues(difValues, 9)
	if len(deaSeq) == 0 {
		return currentDif, math.NaN(), math.NaN()
	}
	dea := deaSeq[len(deaSeq)-1]

	// 第三步：计算MACD柱状图（HIST）= 当前DIF - DEA
	hist := (currentDif - dea) * 2.0 // 乘以2.0以跟随交易所规则

	return currentDif, dea, hist
}

// calculateMACDSequence 计算MACD序列（返回DIF、DEA、HIST三个序列）
// 返回值：(DIF序列, DEA序列, HIST序列)
func calculateMACDSequence(klines []Kline) ([]float64, []float64, []float64) {
	if len(klines) < 26 {
		return nil, nil, nil
	}

	// 第一步：使用增量计算EMA序列（O(n)时间复杂度）
	ema12Seq := calculateEMASequence(klines, 12)
	ema26Seq := calculateEMASequence(klines, 26)

	if len(ema12Seq) == 0 || len(ema26Seq) == 0 {
		return nil, nil, nil
	}

	// 计算DIF序列（从第26根K线开始，因为EMA26需要26根K线）
	difValues := make([]float64, 0, len(ema26Seq))
	ema12StartIdx := len(ema12Seq) - len(ema26Seq)
	
	for i := 0; i < len(ema26Seq); i++ {
		ema12Idx := ema12StartIdx + i
		if ema12Idx >= 0 && ema12Idx < len(ema12Seq) {
			difAtI := ema12Seq[ema12Idx] - ema26Seq[i]
			difValues = append(difValues, difAtI)
		}
	}

	if len(difValues) == 0 {
		return nil, nil, nil
	}

	// 第二步：计算信号线（DEA）= 对DIF序列计算9期EMA
	deaSeq := calculateEMASequenceFromValues(difValues, 9)
	if len(deaSeq) == 0 {
		// 如果无法计算DEA，返回DIF序列，DEA和HIST为nil
		return difValues, nil, nil
	}

	// 第三步：计算MACD柱状图（HIST）= DIF - DEA
	// DEA序列通常比DIF序列短，所以需要对齐
	histValues := make([]float64, 0, len(deaSeq))
	difStartIdx := len(difValues) - len(deaSeq)
	
	for i := 0; i < len(deaSeq); i++ {
		difIdx := difStartIdx + i
		if difIdx >= 0 && difIdx < len(difValues) {
			hist := (difValues[difIdx] - deaSeq[i]) * 2.0 // 乘以2.0以跟随交易所规则
			histValues = append(histValues, hist)
		}
	}

	// 返回对齐后的序列（最后几个值）
	return difValues, deaSeq, histValues
}

// calculateRSISequence 计算RSI序列（增量计算，O(n)时间复杂度）
// 返回每个时间点的RSI值序列
func calculateRSISequence(klines []Kline, period int) []float64 {
	if len(klines) <= period {
		return nil
	}

	sequence := make([]float64, 0, len(klines)-period)
	
	// 计算初始平均涨跌幅
	gains := 0.0
	losses := 0.0
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 计算第一个RSI值
	if avgLoss == 0 {
		sequence = append(sequence, 100)
	} else {
		rs := avgGain / avgLoss
		rsi := 100 - (100 / (1 + rs))
		sequence = append(sequence, rsi)
	}

	// 使用Wilder平滑方法计算后续RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}

		if avgLoss == 0 {
			sequence = append(sequence, 100)
		} else {
			rs := avgGain / avgLoss
			rsi := 100 - (100 / (1 + rs))
			sequence = append(sequence, rsi)
		}
	}

	return sequence
}

// calculateRSI 计算RSI
// 数据不足时返回NaN，调用方需要检查
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return math.NaN()
	}

	gains := 0.0
	losses := 0.0

	// 计算初始平均涨跌幅
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 使用Wilder平滑方法计算后续RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 计算ATR
// 数据不足时返回NaN，调用方需要检查
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return math.NaN()
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// 计算初始ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// getOpenInterestData 获取OI数据（支持多平台）
func getOpenInterestData(symbol string) (*OIData, error) {
	exchangeMutex.RLock()
	apiURL := baseAPIURL
	exchangeMutex.RUnlock()
	
	url := fmt.Sprintf("%s/fapi/v1/openInterest?symbol=%s", apiURL, symbol)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, err := strconv.ParseFloat(result.OpenInterest, 64)
	if err != nil {
		return nil, fmt.Errorf("解析OpenInterest失败: %w", err)
	}

	// 注意：目前只返回最新值，平均值需要历史数据计算
	// 如果后续需要，应该维护历史OI数据来计算平均值
	return &OIData{
		Latest:  oi,
		Average: oi, // 暂时使用最新值作为平均值（需要历史数据才能准确计算）
	}, nil
}

// getFundingRate 获取资金费率（支持多平台）
func getFundingRate(symbol string) (float64, error) {
	exchangeMutex.RLock()
	apiURL := baseAPIURL
	exchangeMutex.RUnlock()
	
	url := fmt.Sprintf("%s/fapi/v1/premiumIndex?symbol=%s", apiURL, symbol)

	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, err := strconv.ParseFloat(result.LastFundingRate, 64)
	if err != nil {
		return 0, fmt.Errorf("解析LastFundingRate失败: %w", err)
	}
	return rate, nil
}

// Format 格式化输出市场数据
func Format(data *Data) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("current_price = %.2f, current_ema20 = %.3f, current_macd = %.3f, current_rsi (7 period) = %.3f\n\n",
		data.CurrentPrice, data.CurrentEMA20, data.CurrentMACD, data.CurrentRSI7))

	sb.WriteString(fmt.Sprintf("In addition, here is the latest %s open interest and funding rate for perps:\n\n",
		data.Symbol))

	if data.OpenInterest != nil {
		sb.WriteString(fmt.Sprintf("Open Interest: Latest: %.2f Average: %.2f\n\n",
			data.OpenInterest.Latest, data.OpenInterest.Average))
	}

	sb.WriteString(fmt.Sprintf("Funding Rate: %.2e\n\n", data.FundingRate))

	if data.IntradaySeries != nil {
		sb.WriteString("Intraday series (oldest → latest):\n\n")

		if len(data.IntradaySeries.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.IntradaySeries.MidPrices)))
		}

		if len(data.IntradaySeries.VolumeValues) > 0 {
			sb.WriteString(fmt.Sprintf("Volume: %s\n\n", formatFloatSlice(data.IntradaySeries.VolumeValues)))
		}

		if len(data.IntradaySeries.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA indicators (20‑period): %s\n\n", formatFloatSlice(data.IntradaySeries.EMA20Values)))
		}

		if len(data.IntradaySeries.DIFValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD DIF (MACD线): %s\n\n", formatFloatSlice(data.IntradaySeries.DIFValues)))
		}

		if len(data.IntradaySeries.DEAValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD DEA (信号线): %s\n\n", formatFloatSlice(data.IntradaySeries.DEAValues)))
		}

		if len(data.IntradaySeries.MACDValues) > 0 {
			sb.WriteString(fmt.Sprintf("MACD HIST (柱状图 = DIF - DEA): %s\n\n", formatFloatSlice(data.IntradaySeries.MACDValues)))
		}

		if len(data.IntradaySeries.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (7‑Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI7Values)))
		}

		if len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI indicators (14‑Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI14Values)))
		}
	}

	return sb.String()
}

// formatFloatSlice 格式化float64切片为字符串
func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = fmt.Sprintf("%.3f", v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

// Normalize 标准化symbol,确保是USDT交易对
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}
