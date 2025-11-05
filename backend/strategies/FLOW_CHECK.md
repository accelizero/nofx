# 策略提示词加载和数据流程验证

## ✅ 完整流程确认

### 1. 动态加载策略提示词

**流程**：
```
config.toml [strategy] 
  → config.Config.Strategy 
  → AutoTraderConfig.StrategyName/StrategyPreference 
  → decision.Context.StrategyName/StrategyPreference 
  → buildSystemPrompt() 
  → LoadStrategyPrompt() 
  → 加载 base_prompt.txt + {strategy_name}/strategy_prompt.txt
```

**代码位置**：
- `backend/pkg/decision/engine.go:118` - `buildSystemPrompt(ctx.StrategyName, ctx.StrategyPreference)`
- `backend/pkg/decision/strategy.go:13` - `LoadStrategyPrompt(strategyName, preference)`
- `backend/pkg/decision/engine.go:285` - 加载策略文件

**包含内容**：
- ✅ base_prompt.txt - 基础规则、风险控制、输出格式
- ✅ strategy_prompt.txt - 策略特定目标和方法
- ✅ 策略偏好说明（conservative/aggressive/balanced）
- ✅ 动态仓位配置（根据账户状态生成）

### 2. 获取市场数据（K线、技术指标）

**流程**：
```
GetFullDecision() 
  → fetchMarketDataForContext(ctx)
  → 获取持仓币种 + 候选币种的市场数据
  → 存储到 ctx.MarketDataMap
```

**代码位置**：
- `backend/pkg/decision/engine.go:98` - `fetchMarketDataForContext(ctx)`
- `backend/pkg/decision/engine.go:137-272` - 获取市场数据逻辑

**包含数据**：
- ✅ 3分钟价格序列（MidPrices）
- ✅ 4小时K线数据
- ✅ EMA20序列
- ✅ MACD序列（DIF、DEA、HIST）
- ✅ RSI7、RSI14序列
- ✅ 成交量序列（Volume）
- ✅ 持仓量序列（OpenInterest）
- ✅ 资金费率（FundingRate）
- ✅ ATR指标（4小时）

### 3. 多时间框架分析

**流程**：
```
buildMultiTimeframePrompt() 
  → MultiTimeframeAnalyzer.Analyze(ctx)
  → 获取日线、4小时、1小时、15分钟数据
  → 计算评分和推荐方向
```

**代码位置**：
- `backend/pkg/decision/engine.go:104` - `buildMultiTimeframePrompt(ctx, mcpClient)`
- `backend/pkg/decision/multiframe_analyzer.go:82` - `Analyze(ctx)`

**包含数据**：
- ✅ 日线（1d）完整数据
- ✅ 4小时（4h）完整数据
- ✅ 1小时（1h）完整数据
- ✅ 15分钟（15m）完整数据
- ✅ 每个时间框架的评分
- ✅ 推荐方向（做多/做空）
- ✅ 一致性评分

### 4. 构建User Prompt（包含所有动态数据）

**代码位置**：
- `backend/pkg/decision/engine.go:329-538` - `buildMultiTimeframePrompt()`

**包含内容**：

#### 4.1 系统状态
- ✅ 当前时间
- ✅ 周期编号
- ✅ 运行时长

#### 4.2 账户状态
- ✅ 总净值
- ✅ 可用余额
- ✅ 总盈亏（金额和百分比）
- ✅ 保证金使用率
- ✅ 持仓数量

#### 4.3 当前持仓（每个持仓包含）
- ✅ 币种、方向、入场价、当前价
- ✅ 盈亏百分比
- ✅ 杠杆、保证金、强平价
- ✅ 持仓时长
- ✅ **多时间框架评分**（做多/做空评分、推荐方向、一致性）
- ✅ **止损/止盈设置**（价格、距离百分比）
- ✅ **进场逻辑**（EntryLogic）
- ✅ **出场逻辑**（ExitLogic）
- ✅ **逻辑有效性检查结果**

#### 4.4 候选币种（按评分排序，每个币种包含）
- ✅ **评分信息**（做多/做空评分、推荐方向、一致性）
- ✅ **日线数据**（完整序列：价格、EMA、MACD、RSI、成交量等）
- ✅ **4小时数据**（完整序列）
- ✅ **1小时数据**（完整序列）
- ✅ **15分钟数据**（完整序列）
- ✅ **详细市场数据**（3分钟序列 + 4小时上下文）

每个时间框架数据包含：
- ✅ MidPrices数组（价格序列）
- ✅ Volume数组（成交量序列）
- ✅ EMA20数组（EMA序列）
- ✅ MACD DIF数组（MACD线序列）
- ✅ MACD DEA数组（信号线序列）
- ✅ MACD HIST数组（柱状图序列）
- ✅ RSI7数组（RSI7序列）
- ✅ RSI14数组（RSI14序列）
- ✅ 持仓量（OpenInterest）
- ✅ 资金费率（FundingRate）
- ✅ ATR指标

#### 4.5 绩效反馈
- ✅ 夏普比率

#### 4.6 风险提示
- ✅ 最近的强制平仓记录

### 5. 发送给AI

**流程**：
```
GetFullDecision() 
  → buildSystemPrompt() (策略提示词 + 动态仓位配置)
  → buildMultiTimeframePrompt() (所有市场数据 + 持仓 + 候选币种)
  → mcpClient.CallWithMessages(systemPrompt, userPrompt)
```

**代码位置**：
- `backend/pkg/decision/engine.go:121` - `mcpClient.CallWithMessages(systemPrompt, userPrompt)`

## ✅ 确认结果

**所有数据都已正确组合并发送给AI**：

1. ✅ **策略提示词** - 从文件动态加载（base + strategy + preference）
2. ✅ **代码逻辑** - 硬约束、风险控制规则（在base_prompt.txt中）
3. ✅ **K线数据** - 多时间框架（日/4h/1h/15m）完整序列
4. ✅ **市场数据** - 价格、EMA、MACD、RSI、成交量、持仓量、资金费率、ATR
5. ✅ **持仓数据** - 价格、盈亏、止损止盈、持仓逻辑、多时间框架评分
6. ✅ **候选币种** - 按评分排序，包含完整的多时间框架数据

## 📋 数据流图

```
配置文件 (config.toml)
  ↓
策略配置 (StrategyConfig)
  ↓
AutoTraderConfig
  ↓
Context (决策上下文)
  ├─ StrategyName/StrategyPreference → buildSystemPrompt()
  │   └─ LoadStrategyPrompt() → base_prompt.txt + strategy_prompt.txt
  │
  ├─ Positions → buildMultiTimeframePrompt()
  ├─ CandidateCoins → buildMultiTimeframePrompt()
  ├─ MarketDataMap → buildMultiTimeframePrompt()
  │   └─ fetchMarketDataForContext() → 获取K线和技术指标
  │
  └─ MultiTimeframeAnalyzer.Analyze()
      └─ 多时间框架分析（日/4h/1h/15m）
          └─ 评分和推荐方向

最终组合：
  SystemPrompt = 策略提示词 + 动态仓位配置
  UserPrompt = 账户状态 + 持仓 + 候选币种 + K线数据 + 市场数据
  
  ↓
CallWithMessages(systemPrompt, userPrompt)
  ↓
AI返回决策
```

## ✅ 验证通过

系统已经能够：
1. ✅ 动态加载策略提示词文件
2. ✅ 结合代码逻辑（硬约束、规则）
3. ✅ 获取并发送K线数据（多时间框架）
4. ✅ 获取并发送市场数据（技术指标序列）
5. ✅ 获取并发送持仓数据（含逻辑和评分）
6. ✅ 获取并发送候选币种数据（含完整K线和技术指标）

所有数据都已正确组合并发送给AI！

