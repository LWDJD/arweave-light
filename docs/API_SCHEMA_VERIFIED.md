# Arweave HTTP API 精确 Schema（实测验证）

> 验证时间: 2026-05-07  
> 验证对象: `https://arweave.net` (生产网关)  
> 区块高度: 1912914–1912915  
> 验证方式: 直接 curl 请求并捕获完整 JSON / 响应头  

---

## `/info` — GET

**11 字段，全为顶层 JSON 键值。** Content-Type: `application/json`.

```json
{
  "version": 5,
  "release": 91,
  "queue_length": 0,
  "peers": 277,
  "node_state_latency": 1,
  "network": "arweave.N.1",
  "height": 1912914,
  "git_hash": "5c6ab811c3ba32d32bc974961d6e87cb3ab17c7b",
  "current": "base64url(64)",
  "cached_blocks": 1918658,
  "blocks": 1912915
}
```

| # | 字段 | 类型 | 说明 |
|---|------|------|------|
| 1 | `version` | int | 协议版本 |
| 2 | `release` | int | 节点软件发行号 |
| 3 | `queue_length` | int | 消息队列长度 |
| 4 | `peers` | int | 已知对等节点数 |
| 5 | `node_state_latency` | int | 节点状态延迟 |
| 6 | `network` | string | 网络标识，如 `arweave.N.1` |
| 7 | `height` | int | 当前区块高度 |
| 8 | `git_hash` | string(40) | Git 提交哈希 |
| 9 | `current` | string(64) | 当前区块的 indep_hash (base64url) |
| 10 | `cached_blocks` | int | 缓存的区块头数量 |
| 11 | `blocks` | int | 总区块数 |

---

## `/height` — GET

**纯文本。** 直接返回当前区块高度整数。

```
1912915
```

| 属性 | 值 |
|------|-----|
| Content-Type | `text/plain; charset=utf-8` |
| 格式 | 纯文本十进制整数 |

---

## `/block/height/{height}` — GET

**51 顶层字段 + 3 层嵌套结构。** 字段按实际返回顺序排列。

### 顶层字段（按实际 JSON key 顺序）

| # | 字段 | 类型 | 说明 |
|---|------|------|------|
| 1 | `replica_format` | int | 副本格式版本 |
| 2 | `packing_difficulty` | int | 打包难度 |
| 3 | `unpacked_chunk_hash` | string(43) | 解压数据块哈希 (base64url) |
| 4 | `unpacked_chunk2_hash` | string(43) | 解压数据块2哈希 (base64url) |
| 5 | `chunk2_hash` | string(43) | 数据块2哈希 (base64url) |
| 6 | `merkle_rebase_support_threshold` | string(15) | Merkle 重组阈值 |
| 7 | `chunk_hash` | string(43) | 数据块哈希 (base64url) |
| 8 | `block_time_history_hash` | string(43) | 出块时间历史哈希 (base64url) |
| 9 | `recall_byte2` | string(15) | 召回字节2 |
| 10 | `hash_preimage` | string(43) | 哈希原像 (base64url) |
| 11 | `recall_byte` | string(15) | 召回字节 |
| 12 | `reward` | string(12) | 本块奖励 (Winston 十进制字符串) |
| 13 | `previous_solution_hash` | string(43) | 前一个解哈希 (base64url) |
| 14 | `partition_number` | int | 分区编号 |
| 15 | `nonce_limiter_info` | object(11) | Nonce 限制器信息 (见下方 §子结构) |
| 16 | `poa2` | object(5) | 访问证明2 (见下方 §子结构) |
| 17 | `signature` | string(683) | 区块签名 (RSA, base64url) |
| 18 | `reward_key` | string(683) | 奖励公钥 (RSA, base64url) |
| 19 | `price_per_gib_minute` | string | 存储价格 (Winston/GiB·min) |
| 20 | `scheduled_price_per_gib_minute` | string | 计划存储价格 |
| 21 | `reward_history_hash` | string(43) | 奖励历史哈希 (base64url) |
| 22 | `debt_supply` | string | 债务供应量 |
| 23 | `kryder_plus_rate_multiplier` | string | Kryder+ 倍率 |
| 24 | `kryder_plus_rate_multiplier_latch` | string | Kryder+ 锁存值 |
| 25 | `denomination` | string | 面额值 |
| 26 | `redenomination_height` | int | 重命名高度 |
| 27 | `double_signing_proof` | object(0) | 双重签名证明 (通常为空对象 `{}`) |
| 28 | `previous_cumulative_diff` | string(16) | 前块累计难度 |
| 29 | `usd_to_ar_rate` | array(2) | USD/AR 汇率对 `[prev, current]` |
| 30 | `scheduled_usd_to_ar_rate` | array(2) | 计划汇率对 |
| 31 | `packing_2_5_threshold` | string | 2.5 打包阈值 |
| 32 | `strict_data_split_threshold` | string(14) | 严格数据分割阈值 |
| 33 | `nonce` | string(2) | 挖矿 nonce |
| 34 | `previous_block` | string(64) | 前区块 indep_hash (base64url) |
| 35 | `timestamp` | int | Unix 时间戳 (秒) |
| 36 | `last_retarget` | int | 上次难度调整时间 |
| 37 | `diff` | string(78) | 难度目标值 (大整数十进制) |
| 38 | `height` | int | 区块高度 |
| 39 | `hash` | string(43) | 区块哈希 (base64url) |
| 40 | `indep_hash` | string(64) | 独立哈希 (base64url，48 字节编码为 64 字符) |
| 41 | `txs` | array(string) | 交易 ID 列表 |
| 42 | `tx_root` | string(0 \| 64) | 交易 Merkle 根 (空字符串或 64 字符 base64url) |
| 43 | `wallet_list` | string(64) | 钱包状态 Merkle 根 (base64url) |
| 44 | `reward_addr` | string(43) | 矿工奖励地址 (base64url) |
| 45 | `tags` | array(object) | 区块标签 (`[{"name":"...","value":"..."}]`) |
| 46 | `reward_pool` | string(18) | 奖励池余额 (Winston 十进制) |
| 47 | `weave_size` | string(15) | 总 weave 数据大小 (字节十进制) |
| 48 | `block_size` | string | 区块大小 |
| 49 | `cumulative_diff` | string(16) | 累计难度 |
| 50 | `hash_list_merkle` | string(64) | 历史区块哈希 Merkle 根 (base64url) |
| 51 | `poa` | object(5) | 访问证明 (见下方 §子结构) |

---

### 子结构：`poa`（5 字段）

```json
{
  "option": "1",
  "tx_path": "base64url(...)",
  "data_path": "base64url(...)",
  "chunk": "base64url(...)",
  "unpacked_chunk": "base64url(...)"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `option` | string | PoA 选项 (通常 `"1"`) |
| `tx_path` | string | 交易路径 Merkle 证明 (base64url) |
| `data_path` | string | 数据路径 Merkle 证明 (base64url) |
| `chunk` | string | 数据块内容 (base64url) |
| `unpacked_chunk` | string | 解压后的数据块 (base64url) |

---

### 子结构：`poa2`（5 字段）

与 `poa` 完全相同，5 字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `option` | string | PoA 选项 |
| `tx_path` | string | 交易路径 (base64url) |
| `data_path` | string | 数据路径 (base64url) |
| `chunk` | string | 数据块内容 (base64url) |
| `unpacked_chunk` | string | 解压数据块 (base64url) |

---

### 子结构：`nonce_limiter_info`（11 字段）

```json
{
  "output": "string(43)",
  "global_step_number": 103624781,
  "seed": "string(85)",
  "next_seed": "string(85)",
  "zone_upper_bound": 387065091760374,
  "next_zone_upper_bound": 387065401090294,
  "prev_output": "string(43)",
  "last_step_checkpoints": ["...", "..."],
  "checkpoints": ["...", "..."],
  "vdf_difficulty": "1107811",
  "next_vdf_difficulty": "1107811"
}
```

| # | 字段 | 类型 | 说明 |
|---|------|------|------|
| 1 | `output` | string(43) | VDF 输出 (base64url) |
| 2 | `global_step_number` | int | 全局 VDF 步数 |
| 3 | `seed` | string(85) | VDF 种子 (base64url) |
| 4 | `next_seed` | string(85) | 下一 VDF 种子 (base64url) |
| 5 | `zone_upper_bound` | int | 区域上界 |
| 6 | `next_zone_upper_bound` | int | 下一区域上界 |
| 7 | `prev_output` | string(43) | 前一个 VDF 输出 (base64url) |
| 8 | `last_step_checkpoints` | array(string) | 最后步骤检查点列表 |
| 9 | `checkpoints` | array(string) | 检查点列表 |
| 10 | `vdf_difficulty` | string | 当前 VDF 难度 |
| 11 | `next_vdf_difficulty` | string | 下一 VDF 难度 |

---

### 子结构：`double_signing_proof`

```json
{}
```

空对象 (`dict(0 keys)`)。有双重签名事件时才包含数据。

---

## `/block/hash/{hash}` — GET

响应格式与 `/block/height/{height}` 完全相同。

| 属性 | 值 |
|------|-----|
| 参数 | `{hash}` 为完整的 indep_hash (64 字符 base64url) |
| 缩略哈希 | 返回 404 |
| Content-Type | `application/json` |

---

## `/tx/{id}` — GET

**13 字段。** Content-Type: `application/json`.

```json
{
  "format": 2,
  "id": "base64url(43)",
  "last_tx": "base64url(64)",
  "owner": "string(683, RSA public key)",
  "tags": [
    {"name": "base64url", "value": "base64url"}
  ],
  "target": "",
  "quantity": "0",
  "data": "base64url or empty",
  "data_size": "749484",
  "data_tree": [],
  "data_root": "base64url(43)",
  "reward": "4393360141",
  "signature": "string(683)"
}
```

| # | 字段 | 类型 | 说明 |
|---|------|------|------|
| 1 | `format` | int | 交易格式版本 (1 或 2) |
| 2 | `id` | string(43) | 交易 ID (base64url) |
| 3 | `last_tx` | string(64) | 上一笔交易 ID (base64url) |
| 4 | `owner` | string(683) | 所有者 RSA 公钥 (base64url) |
| 5 | `tags` | array(object) | Tag 数组，每个元素 `{"name":"...","value":"..."}` |
| 6 | `target` | string | 目标地址 (base64url, 空字符串表示无目标) |
| 7 | `quantity` | string | 转账金额 (Winston 十进制字符串) |
| 8 | `data` | string | 交易数据 (base64url 编码，可为空字符串) |
| 9 | `data_size` | string | 数据大小 (字节十进制字符串) |
| 10 | `data_tree` | array | 数据 Merkle 树 (通常为空数组 `[]`) |
| 11 | `data_root` | string(43) | 数据 Merkle 根 (base64url) |
| 12 | `reward` | string | 矿工费 (Winston 十进制字符串) |
| 13 | `signature` | string(683) | 交易签名 (RSA, base64url) |

### Tags 格式

```json
[
  {"name": "Q29udGVudC1UeXBl", "value": "dGV4dC9odG1s"},
  {"name": "QXBwLU5hbWU", "value": "U29tZUFwcA"}
]
```

- `name` 和 `value` 均为 **base64url 编码**的字节串
- 常见 tag: `Content-Type` (base64url of `"Content-Type"`)
- 解码后 `name` / `value` 是任意 UTF-8 字符串

---

## `/tx/{id}/status` — GET

**3 字段。** Content-Type: `application/json`.

```json
{
  "block_height": 1911424,
  "block_indep_hash": "base64url(64)",
  "number_of_confirmations": 1492
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `block_height` | int | 交易所在区块高度 |
| `block_indep_hash` | string(64) | 区块独立哈希 (base64url) |
| `number_of_confirmations` | int | 确认数 |

| HTTP 状态 | 含义 |
|-----------|------|
| `200` | 已确认，返回上述 JSON |
| `202 Pending` | 交易在内存池中待确认 (纯文本) |
| `404` | 交易未找到 |

---

## `/tx/{id}/data` — GET

返回交易 `data` 字段的**原始字节**。Content-Type 由交易的 `Content-Type` tag 决定。

---

## `/price/{bytes}` — GET

**纯文本。** 返回 Winston 整数。

```
1527512677
```

| 属性 | 值 |
|------|-----|
| Content-Type | `text/plain; charset=utf-8` |
| 参数 | `{bytes}` 为数据大小的十进制整数 |
| 返回 | 预估交易费 (Winston 十进制字符串) |

---

## `/price/{bytes}/{addr}` — GET

同 `/price/{bytes}`，但当 `{addr}` 地址不存在于区块链上时，额外计入新钱包创建费用。

---

## `/inflation/{height}` — GET

**纯文本。** 返回 Winston 整数。

```
186812030036
```

| 属性 | 值 |
|------|-----|
| Content-Type | `text/plain; charset=utf-8` |
| 参数 | `{height}` 为区块高度 |
| 返回 | 该高度对应的通胀奖励 (Winston 十进制) |

---

## `/peers` — GET

**JSON 字符串数组。** 地址格式为裸 `ip:port`，**不带** `http://` 前缀。

```json
["128.241.227.87:5984", "49.12.173.84:1984", "65.21.217.222:1984"]
```

| 属性 | 值 |
|------|-----|
| Content-Type | `application/json` |
| 格式 | `"ip:port"` (无协议前缀) |

> ⚠️ **注意**: `arweave.net` 网关返回的格式是 `ip:port`，与标准 Erlang 节点源码中返回的 `http://ip:port` 不同。网关层可能做了去协议前缀处理。

---

## `/tx_anchor` — GET

**纯文本。** 返回 base64url 编码的区块哈希，用作交易锚点。

```
SFI0Bw_-2hN_KI2EYlaEzFoPTGiOsW35qHnh6Irc12uh5GCL0lFar05YZmNju7cM
```

| 属性 | 值 |
|------|-----|
| Content-Type | `text/plain; charset=utf-8` |
| 长度 | 64 字符 (48 字节编码为 base64url) |
| 用途 | 新建交易时作为 `last_tx` 字段的值 |
