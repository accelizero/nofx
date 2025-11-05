# Backend 架构重构说明

## 📋 概述

本次重构将Go后端代码迁移到 `backend` 目录，并将数据存储从文件系统迁移到SQLite数据库。

## 🏗️ 目录结构

```
backend/
├── pkg/                 # 后端核心包（所有后端逻辑）
│   ├── db/              # 数据库抽象层
│   │   └── db.go        # 数据库管理器，支持多个SQLite数据库文件
│   ├── storage/         # 存储层（替换文件存储）
│   │   ├── adapter.go                    # 存储适配器，统一管理所有存储模块
│   │   ├── position_logic.go             # 持仓逻辑存储
│   │   ├── position_logic_wrapper.go     # 持仓逻辑包装器（兼容旧接口）
│   │   ├── trade.go                      # 交易记录存储
│   │   ├── cycle_snapshot.go             # 周期快照存储
│   │   └── cache.go                      # 缓存存储
│   ├── api/             # API服务器
│   ├── manager/         # Trader管理器
│   ├── trader/          # 交易器实现
│   ├── decision/        # 决策引擎
│   ├── market/          # 市场数据
│   ├── pool/            # 币种池
│   ├── logger/          # 日志记录
│   ├── config/          # 配置管理
│   └── mcp/             # MCP客户端
└── README.md            # 本文件
```

## 🗄️ 数据库设计

为了确保数据不卡顿，不同的逻辑使用不同的数据库文件：

| 数据库文件 | 用途 | 存储内容 |
|-----------|------|---------|
| `position_logic.db` | 持仓逻辑 | 进场/出场逻辑、止损/止盈价格 |
| `trade_history.db` | 交易记录 | 完整的交易历史（开仓+平仓） |
| `cycle_snapshots.db` | 周期快照 | 每个周期的完整状态快照 |
| `cache.db` | 缓存数据 | 多时间框架缓存等临时数据 |

### 数据库连接管理

- 每个数据库文件使用独立的SQLite连接
- SQLite建议每个数据库文件只使用一个连接（`MaxOpenConns=1`）
- 启用外键约束
- 自动创建数据库目录和表结构

## 📦 存储模块说明

### 1. PositionLogicStorage（持仓逻辑存储）

**功能：**
- 保存进场逻辑和出场逻辑
- 保存止损和止盈价格
- 查询持仓逻辑
- 删除持仓逻辑（平仓后）

**表结构：**
```sql
CREATE TABLE position_logic (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    symbol TEXT NOT NULL,
    side TEXT NOT NULL,
    entry_logic TEXT,
    exit_logic TEXT,
    stop_loss REAL DEFAULT 0,
    take_profit REAL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(symbol, side)
);
```

### 2. TradeStorage（交易记录存储）

**功能：**
- 记录完整的交易（开仓+平仓配对）
- 按日期查询交易
- 查询最近N笔交易
- 按币种查询交易

**表结构：**
```sql
CREATE TABLE trades (
    trade_id TEXT PRIMARY KEY,
    symbol TEXT NOT NULL,
    side TEXT NOT NULL,
    -- 开仓信息
    open_time DATETIME NOT NULL,
    open_price REAL NOT NULL,
    open_quantity REAL NOT NULL,
    open_leverage INTEGER NOT NULL,
    open_order_id INTEGER NOT NULL,
    open_reason TEXT,
    open_cycle_num INTEGER NOT NULL,
    -- 平仓信息
    close_time DATETIME NOT NULL,
    close_price REAL NOT NULL,
    close_quantity REAL NOT NULL,
    close_order_id INTEGER NOT NULL,
    close_reason TEXT,
    close_cycle_num INTEGER NOT NULL,
    is_forced INTEGER NOT NULL DEFAULT 0,
    forced_reason TEXT,
    -- 交易结果
    duration TEXT,
    position_value REAL NOT NULL,
    margin_used REAL NOT NULL,
    pnl REAL NOT NULL,
    pnl_pct REAL NOT NULL,
    was_stop_loss INTEGER NOT NULL DEFAULT 0,
    success INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

### 3. CycleSnapshotStorage（周期快照存储）

**功能：**
- 记录每个周期的完整状态快照
- 查询周期快照列表
- 根据周期编号查询快照

**表结构：**
```sql
CREATE TABLE cycle_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trader_id TEXT NOT NULL,
    cycle_number INTEGER NOT NULL,
    timestamp DATETIME NOT NULL,
    scan_interval INTEGER NOT NULL,
    snapshot_data TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(trader_id, cycle_number)
);
```

### 4. CacheStorage（缓存存储）

**功能：**
- 缓存数据（带TTL）
- 自动清理过期缓存
- 支持任意JSON数据

**表结构：**
```sql
CREATE TABLE cache (
    cache_key TEXT PRIMARY KEY,
    cache_data TEXT NOT NULL,
    timestamp DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
);
```

## 🔄 迁移计划

### 阶段1：创建新的存储系统 ✅
- [x] 创建数据库抽象层
- [x] 创建各个存储模块
- [x] 创建存储适配器

### 阶段2：兼容性适配（进行中）
- [ ] 创建包装器，兼容旧接口
- [ ] 更新现有代码使用新存储系统
- [ ] 数据迁移工具（可选）

### 阶段3：完全迁移
- [ ] 移除文件存储代码
- [ ] 更新所有导入路径
- [ ] 测试验证

## 🚀 使用方法

### 初始化存储系统

```go
import "nofx/backend/pkg/storage"

// 创建存储适配器
adapter, err := storage.NewStorageAdapter("data")
if err != nil {
    log.Fatalf("初始化存储系统失败: %v", err)
}
defer adapter.Close()

// 获取各个存储模块
positionLogicStorage := adapter.GetPositionLogicStorage()
tradeStorage := adapter.GetTradeStorage()
cycleSnapshotStorage := adapter.GetCycleSnapshotStorage()
cacheStorage := adapter.GetCacheStorage()
```

### 使用持仓逻辑存储

```go
// 保存进场逻辑
entryLogic := &storage.EntryLogic{
    Reasoning: "基于技术分析，建议做多",
    Timestamp: time.Now(),
}
err := positionLogicStorage.SaveEntryLogic("BTCUSDT", "long", entryLogic)

// 获取持仓逻辑
logic, err := positionLogicStorage.GetLogic("BTCUSDT", "long")
```

### 使用交易记录存储

```go
// 记录交易
trade := &storage.TradeRecord{
    TradeID: "BTCUSDT_long_20240101_120000",
    Symbol: "BTCUSDT",
    Side: "long",
    OpenTime: time.Now(),
    // ... 其他字段
}
err := tradeStorage.LogTrade(trade)

// 查询最近10笔交易
trades, err := tradeStorage.GetLatestTrades(10)
```

## 📝 注意事项

1. **数据库文件位置**：默认存储在 `data/` 目录下，可通过 `NewStorageAdapter` 的参数指定
2. **并发安全**：所有存储模块都是并发安全的
3. **性能优化**：每个数据库文件使用独立连接，避免锁竞争
4. **数据迁移**：如果需要从旧的文件存储迁移数据，可以编写迁移脚本

## 🔧 依赖

- `modernc.org/sqlite` - 纯Go实现的SQLite驱动（无需CGO）

