# 交易策略文件夹

本文件夹包含交易策略的提示词文件。

## 结构

- `base_prompt.txt` - 基础提示词（通用的交易规则、约束、输出格式等）
- `{strategy_name}/strategy_prompt.txt` - 具体策略的提示词（策略特定的目标、方法等）

## 使用方式

在 `config.toml` 中配置策略：

```toml
[strategy]
  name = "sharpe_ratio"  # 策略名称（对应文件夹名）
  preference = "conservative"  # 策略偏好（可选）
```

## 创建新策略

1. 在 `strategies` 文件夹下创建新文件夹，例如 `my_strategy`
2. 在该文件夹中创建 `strategy_prompt.txt` 文件
3. 编写策略特定的提示词内容
4. 在 `config.toml` 中设置 `strategy.name = "my_strategy"`

## 策略偏好

策略偏好（preference）用于个性化定制策略行为，当前支持：
- `conservative` - 保守模式：更严格的开仓标准、更小的仓位、更严格的止损
- `aggressive` - 激进模式：相对宽松的开仓标准、更大的仓位、更灵活的止损
- `balanced` - 平衡模式：标准设置

## 当前可用策略

- `sharpe_ratio` - 夏普比率最大化策略

