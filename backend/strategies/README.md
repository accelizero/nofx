# 交易策略文件夹

本文件夹包含交易策略的提示词文件。

## 结构

每个策略是一个独立的 `.txt` 文件，例如：
- `base_prompt.txt` - 基础提示词（通用的交易规则、约束、输出格式等）
- `my_strategy.txt` - 自定义策略提示词

## 使用方式

在 `config.toml` 中配置策略：

```toml
[strategy]
  name = "base_prompt"  # 策略名称（对应文件名，不含.txt扩展名）
```

## 创建新策略

1. 在 `strategies` 文件夹下创建新的 `.txt` 文件，例如 `my_strategy.txt`
2. 编写完整的策略提示词内容
3. 在 `config.toml` 中设置 `strategy.name = "my_strategy"`（不含.txt扩展名）

## 当前可用策略

- `base_prompt` - 基础提示词策略

