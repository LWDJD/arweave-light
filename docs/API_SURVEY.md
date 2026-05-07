# Arweave HTTP API 端点调研

> 来源: [ar_http_iface_middleware.erl](https://github.com/ArweaveTeam/arweave/blob/master/apps/arweave/src/ar_http_iface_middleware.erl) (Arweave 主仓库)  
> 补充: [goar Client](https://github.com/everFinance/goar) —— Go SDK 消费端视角  
> 目标: 为 arweave-light 轻节点实现兼容 API 做准备

---

## 一、信息查询类 (GET / HEAD)

这些端点从节点读取数据，不修改状态。

| 端点 | 方法 | 参数 | 响应说明 | 轻节点能否实现 |
|------|------|------|----------|----------------|
| `/` | GET | 无 | 等同于 `/info`，返回网络信息 JSON | ✅ 已有 (`/info`) |
| `/info` | GET | 无 | 11 字段 JSON: `version`(int), `release`(int), `queue_length`(int), `peers`(int), `node_state_latency`(int), `network`(string), `height`(int), `git_hash`(string40), `current`(string64), `cached_blocks`(int), `blocks`(int) — 详细 schema 见 `API_SCHEMA_VERIFIED.md` | ✅ 已有，需补齐全部 11 字段 |
| `/info` | HEAD | 无 | 同 GET，无 body (负载均衡器健康检查) | ✅ 可做 |
| `/` | HEAD | 无 | 同 HEAD `/info` | ✅ 可做 |
| `/height` | GET/HEAD | 无 | 返回当前区块高度 (纯文本整数)；未加入返回 `500` | ✅ 可做 |
| `/time` | GET | 无 | 返回 UTC Unix 时间戳 (秒，纯文本) | ✅ 可做，直接用本地时钟 |
| `/recent` | GET | 无 | `{"blocks":[{"id":"...","received":"...","height":...}],"forks":[...]}` — 最近区块和分叉摘要 | ⚠️ 需修改：轻节点可返回本地存储的最近区块，forks 信息有限 |
| `/peers` | GET | 无 | `["ip:port","ip:port",...]` — 已知的公共 peer 列表。注意 arweave.net 返回裸 `ip:port`（**不带 `http://` 前缀**），与标准 Erlang 节点不同 | ✅ 已有 |
| `/tx_anchor` | GET | 无 | 一个 Base64URL 编码的区块哈希 (纯文本)，用作交易锚点 | ✅ 已有 |
| `/tx/{id}` | GET | `id`: TxID (Base64URL) | 完整交易 JSON — **13 字段** (format, id, last_tx, owner, tags, target, quantity, data, data_size, data_tree, data_root, reward, signature)。详见 `API_SCHEMA_VERIFIED.md` | ✅ 已有 |
| `/tx/{id}/status` | GET | `id`: TxID | **3 字段 JSON**: `block_height`(int), `block_indep_hash`(string64), `number_of_confirmations`(int) — 非数组。已确认→200, 待确认→202 Pending, 未找到→404 | ✅ 可做 (转发或本地查) |
| `/tx/{id}/{field}` | GET | `id`: TxID, `field`: `id|last_tx|owner|tags|target|quantity|data|signature|reward` | 返回该字段的值；`tags` 返回 JSON 数组 `[{"name":"...","value":"..."}]`；`data` 返回原始字节 (可以是 HTML 或其他) | ✅ 可做 (转发) |
| `/tx/{id}/data` | GET | `id`: TxID | 交易 data 字段的原始字节 (content-type 可能为 HTML) | ✅ 已有 |
| `/tx/{id}/data.{ext}` | GET | `id`: TxID, `ext`: 文件扩展名 | 同上，但用于浏览器渲染 (如 `.html`) | ✅ 可做 |
| `/tx/{id}/offset` | GET | `id`: TxID | `{"offset":"...","size":"..."}` — 交易数据在 weave 中的绝对偏移量和字节数 | ⚠️ 需转发全节点 |
| `/tx/pending` | GET | 无 | `["txid1","txid2",...]` — 内存池中所有待确认交易 ID | ❌ 轻节点不维护内存池 |
| `/unconfirmed_tx/{id}` | GET | `id`: TxID | 同 `/tx/{id}` 但包括未确认交易 | ❌ 需内存池 |
| `/block/height/{height}` | GET | `height`: 整数 | 完整区块 JSON — **51 顶层字段** + poa(5)、poa2(5)、nonce_limiter_info(11) 等嵌套结构。详见 `API_SCHEMA_VERIFIED.md` | ✅ 已有 |
| `/block/hash/{hash}` | GET | `hash`: Base64URL 编码的 indep_hash | 同上 | ✅ 需补充 |
| `/block/current` | GET | 无 | 重定向到 `/block/hash/{current_hash}` | ✅ 可做 |
| `/block/{type}/{id}/{field}` | GET | `type`: `height` 或 `hash`, `id`: 高度或哈希, `field`: `wallet_list|hash_list|tx_root|...` | 返回区块的指定字段 | ⚠️ 部分可实现 (如 `hash_list` 需转发) |
| `/block/height/{height}/wallet/{addr}/balance` | GET | 见路径 | 返回指定高度下某地址的余额 (纯文本整数 Winston) | ⚠️ 需修改：轻节点可验证但不能独立计算 |
| `/hash_list` | GET | 无 | 完整 block_index (JSON)，2.6 分叉后已禁用 | ❌ 已废弃 |
| `/hash_list/{from}/{to}` | GET | `from`, `to`: 高度范围 | 指定范围的 block_index (JSON) | ⚠️ 可部分实现 |
| `/block_index` | GET | 无 | 同 `/hash_list` | ❌ 已废弃 |
| `/block_index/{from}/{to}` | GET | 同 `/hash_list/{from}/{to}` | ⚠️ 可部分实现 |
| `/block_index2` / `/hash_list2` | GET | 同上 | 二进制编码版本 | ⚠️ 可部分实现 |
| `/recent_hash_list` | GET | 无 | `["h1","h2",...]` — 最近区块锚点列表 | ⚠️ 需修改：轻节点只维护有限窗口 |
| `/recent_hash_list_diff` | GET | 请求体: 编码的 hash list | 返回本地 hash list 与请求 hash list 的差异 (用于快速同步) | ❌ 轻节点不需要 |
| `/wallet/{addr}/balance` | GET | `addr`: Base64URL 钱包地址 | 余额 (纯文本整数 Winston) | ✅ 已有 |
| `/wallet/{addr}/last_tx` | GET | `addr`: 同上 | 该钱包最后一笔交易 ID (纯文本 Base64URL) | ✅ 可做 |
| `/wallet/{addr}/reserved_rewards_total` | GET | `addr`: 同上 | 该地址的保留挖矿奖励总和 (纯文本整数) | ⚠️ 需修改：需状态树 |
| `/wallet_list` | GET | 无 | 当前区块的 wallet_list (重定向到 `/block/hash/{H}/wallet_list`) | ❌ 轻节点不适合 |
| `/wallet_list/{root_hash}` | GET | `root_hash`: MPT 根哈希 | 第一页钱包列表 (最多 `WALLET_LIST_CHUNK_SIZE` 条) | ❌ 需完整状态树 |
| `/wallet_list/{root_hash}/{cursor}` | GET | 分页游标 | 下一页 | ❌ 同上 |
| `/wallet_list/{root_hash}/{addr}/balance` | GET | 地址和根 | 指定根下某地址余额 | ❌ 同上 |
| `/peers` | GET | 无 | peer URL 列表 | ✅ 已有 |
| `/inflation/{height}` | GET | `height`: 高度 | 该高度的通胀奖励 (纯文本整数 Winston) | ✅ 可做 (算法是确定性的) |
| `/price/{bytes}` | GET | `bytes`: 数据大小(字节) | 纯文本 Winston 整数，Content-Type: `text/plain; charset=utf-8`。**非 JSON** | ✅ 可做 |
| `/price/{bytes}/{addr}` | GET | `bytes`, `addr`: 目标地址 | 含新钱包费 (若地址不存在) 的预估交易费 | ⚠️ 需修改：需查地址是否存在 |
| `/price2/{bytes}` / `/{addr}` | GET | 同上 | 同 price，但始终返回 JSON `{"price":"..."}` | ✅ 可做 |
| `/optimistic_price/{bytes}` / `/{addr}` | GET | 同上 | 乐观定价 (低价) | ⚠️ 需了解算法 |
| `/v2price/{bytes}` / `/{addr}` | GET | 同上 | 新定价方案 (v2) | ⚠️ 需算法 |
| `/reward_history/{block_hash}` | GET | `block_hash`: Base64URL | 二进制编码的奖励历史 (2.6 分叉后) | ❌ 需完整区块数据 |
| `/block_time_history/{block_hash}` | GET | `block_hash`: Base64URL | 二进制编码的区块时间历史 (2.7 分叉后) | ❌ 需完整区块数据 |
| `/total_supply` | GET | 无 | 总供应量 (纯文本整数 Winston) | ⚠️ 需修改：需状态树遍历 |
| `/chunk/{offset}` | GET | `offset`: 绝对字节偏移 | `{"tx_path":"...","chunk":"...","data_path":"..."}` JSON — 打包的 chunk 和证明 | ❌ 需存储模块 |
| `/chunk2/{offset}` | GET | 同上 | 同上，二进制编码 | ❌ 同上 |
| `/chunk_proof/{offset}` | GET | 同上 | chunk 的 Merkle 证明 (JSON) | ❌ 同上 |
| `/chunk_proof2/{offset}` | GET | 同上 | 同上，二进制 | ❌ 同上 |
| `/tx/{id}/chunks` | GET | `id`: TxID | 交易的所有 chunk 哈希列表 (JSON) | ⚠️ 可转发 |
| `/unconfirmed_chunk/{txid}/{offset}` | GET | 同上 | 未确认交易的 chunk | ❌ 需内存池 |
| `/data_roots/{offset}` | GET | `offset`: 绝对字节偏移 | 该偏移所在区块的 data root 条目 (二进制) | ❌ 需存储 |
| `/data_sync_record` | GET | 无 | 数据同步记录 (随机子集) | ❌ 需存储 |
| `/data_sync_record/{start}/{limit}` | GET | 区间参数 | 数据同步记录区间 | ❌ 同上 |
| `/data_sync_record/{start}/{end}/{limit}` | GET | 同上 | 同上 | ❌ 同上 |
| `/sync_buckets` | GET | 无 | 同步桶信息 (二进制) | ❌ 需存储 |
| `/footprint_buckets` | GET | 无 | 足迹桶信息 (二进制) | ❌ 需存储 |
| `/footprints/{partition}/{footprint}` | GET | 分区和足迹编号 | 该足迹的数据存在区间 | ❌ 需存储模块 |
| `/jobs` / `/jobs/{prev_output}` | GET | `prev_output`: 上一个 VDF 输出 | `{"jobs":[...],"partial_diff":"...","next_seed":"...",...}` — VDF 步骤信息 | ❌ 需 VDF 引擎 |
| `/vdf` / `/vdf2` | GET | 无 | VDF 更新 (给配置的 VDF 客户端) | ❌ 需 VDF |
| `/vdf/session` / `/vdf2/session` / `/vdf3/session` / `/vdf4/session` | GET | 无 | 当前 VDF 会话 | ❌ 需 VDF |
| `/vdf/previous_session` / `/vdf2/previous_session` / `/vdf4/previous_session` | GET | 无 | 上一个 VDF 会话 | ❌ 需 VDF |
| `/coordinated_mining/partition_table` | GET | 需 `X-CM-API-Secret` header | CM 分区表 JSON | ❌ 需 CM |
| `/coordinated_mining/state` | GET | 同上 | CM 公开状态 | ❌ 需 CM |
| `/{txid}` / `/{txid}.{ext}` | GET | `txid`: 43 字符 Base64URL | 同 `/tx/{id}/data` (无路径前缀时默认按 tx data 处理) | ✅ 已有 |
| `/is_tx_blacklisted/{txid}` | GET | `txid`: 编码 | 检查交易是否在黑名单 | ⚠️ 可做 (轻量) |
| `/queue` | GET | 无 | **已废弃** — 返回 `[]` | ❌ 废弃 |
| `/current_block` | GET | 无 | **已废弃** — 重定向到 `/block/current` | ✅ 可做 |

---

## 二、数据提交类 (POST)

这些端点向网络提交数据。

| 端点 | 方法 | 参数 | 响应说明 | 轻节点能否实现 |
|------|------|------|----------|----------------|
| `/tx` | POST | JSON 编码的完整交易 (含签名) | `200 OK` 空 body 或错误信息；交易被广播到网络 | ✅ 可做 (代理转发到全节点) |
| `/tx2` | POST | 二进制编码的完整交易 | 同上 | ✅ 可做 (代理) |
| `/chunk` | POST | JSON 编码的 chunk 数据 (含 proof 和 data_root) | `200 OK` 或错误 | ⚠️ 可代理转发 |
| `/block` | POST | JSON 编码的完整新区块 | `200 OK` — 接收区块并广播 | ❌ 轻节点不参与区块传播 |
| `/block2` | POST | 二进制编码的完整新区块 | 同上 | ❌ 同上 |
| `/block_announcement` | POST | 编码的区块公告 | `200` (可含缺失交易列表), `208` (已处理), `412` (无前区块) | ❌ 同上 |
| `/data_roots/{offset}` | POST | 编码的 data roots | 存储 data roots | ❌ 需存储模块 |
| `/peers` | POST | 无 | **已废弃** — 原用于让对端学习你的 IP | ✅ 可做 (空响应) |
| `/wallet` | POST | 需 `X-Internal-Api-Secret` header | 生成新钱包，返回 `{"wallet_address":"...","wallet_access_code":"..."}` | ❌ 内部 API，不安全 |
| `/unsigned_tx` | POST | 需内部密钥，JSON 含 `wallet_access_code` 和 tx 字段 | 签名并提交交易，返回 `{"id":"..."}` | ❌ 内部 API |
| `/vdf` | POST | VDF 更新数据 | 接收 VDF 更新 | ❌ 需 VDF |
| `/partial_solution` | POST | 部分 PoW 解 | 验证并可能接受部分解 (矿池用) | ❌ 需矿池/挖矿 |
| `/pool_cm_jobs` | POST | CM 任务数据 | 矿池-CM 交互 | ❌ 需 CM |
| `/coordinated_mining/h1` | POST | 需 CM API 密钥 | 哈希 1 提交 | ❌ 需 CM |
| `/coordinated_mining/h2` | POST | 需 CM API 密钥 | 哈希 2 提交 | ❌ 需 CM |
| `/coordinated_mining/publish` | POST | 需 CM API 密钥 | CM 发布 | ❌ 需 CM |

---

## 三、挖矿类 (Coordinated Mining / VDF)

| 端点 | 方法 | 参数 | 响应说明 | 轻节点能否实现 |
|------|------|------|----------|----------------|
| `/jobs` | GET | 可选 `prev_output` | 最新 VDF 步骤 + 难度信息 | ❌ |
| `/jobs` | POST (pool_cm_jobs) | CM 任务 | 矿池-CM 任务提交 | ❌ |
| `/partial_solution` | POST | 部分 PoW 解 | 提交部分解到矿池 | ❌ |
| `/vdf` | GET/POST | 见上 | VDF 更新/会话管理 | ❌ |
| `/coordinated_mining/*` | GET/POST | 需 CM API 密钥 | CM 内部协议 | ❌ |

---

## 四、GraphQL / ArQL (特殊)

这些端点由独立 handler 处理（不在 `ar_http_iface_middleware.erl` 中），由 Arweave Gateway 或 ardb 等服务提供。

| 端点 | 方法 | 参数 | 响应说明 | 轻节点能否实现 |
|------|------|------|----------|----------------|
| `/graphql` | POST | `{"query":"..."}` | GraphQL 查询结果 (JSON) | ✅ 已有 (代理转发到全节点) |
| `/arql` | POST | 纯文本 ArQL 查询 | **已废弃** — 返回匹配的交易 ID 列表 | ❌ 已废弃，用 GraphQL 替代 |

## 五、CORS 预检 (OPTIONS)

这些端点返回 CORS 头，便于浏览器访问。

| 端点 | 方法 | 响应说明 | 轻节点能否实现 |
|------|------|----------|----------------|
| `/block` (any) | OPTIONS | `access-control-allow-methods: GET, POST` | ✅ 可做 |
| `/tx` (any) | OPTIONS | `access-control-allow-methods: GET, POST` | ✅ 可做 |
| `/peer*` | OPTIONS | `access-control-allow-methods: GET, POST` | ✅ 可做 |
| 其他 | OPTIONS | `access-control-allow-methods: GET` | ✅ 可做 |

---

## 六、关于 `/ar_price` 和 `/ar_price_list`

**在当前 Arweave 源码中不存在这两个端点。** 可能来自：
- 早期版本的 API（已被 `/price` 系列替代）
- 第三方网关添加的自定义端点
- 文档混淆（实际对应 `/price/{bytes}` 和 `/price2/{bytes}`）

如需了解 Arweave 代币价格，可：
- 通过 GraphQL 查询网关的 `arPrice` 字段
- 通过 `/info` 中的 `usd_to_ar_rate` (部分节点返回)

---

## 七、附录：关键响应格式详解

### 5.1 `/info` 响应

共 **11 字段**，详细 schema 见 `API_SCHEMA_VERIFIED.md`。

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

| 字段 | 类型 | 说明 |
|------|------|------|
| `version` | int | 协议版本 |
| `release` | int | 节点软件发行号 |
| `queue_length` | int | 消息队列长度 |
| `peers` | int | 已知对等节点数 |
| `node_state_latency` | int | 节点状态延迟 |
| `network` | string | 网络标识 |
| `height` | int | 当前区块高度 |
| `git_hash` | string(40) | Git 提交哈希 |
| `current` | string(64) | 当前区块 indep_hash |
| `cached_blocks` | int | 缓存的区块头数量 |
| `blocks` | int | 总区块数 |

**arweave-light 当前返回:**
```json
{
  "height": 1234567,
  "current": "indep_hash_base64url..."
}
```

**需要补齐的字段:** `version`, `release`, `queue_length`, `peers`, `node_state_latency`, `network`, `git_hash`, `cached_blocks`, `blocks`。轻节点可返回：
- `network`: 硬编码或从对等节点获取
- `version`, `release`: 轻节点自身版本
- `blocks`: 本地存储的区块头数量
- `cached_blocks`: 同 blocks
- `peers`: 从 peer pool 获取
- `queue_length`: `0` (无消息队列)
- `node_state_latency`: `0` (无实际测量)
- `git_hash`: 轻节点自身 Git 提交哈希

### 5.2 `/block/height/{height}` 响应

共 **51 顶层字段** + 3 层嵌套子结构。完整 schema 见 `API_SCHEMA_VERIFIED.md`。

**顶层字段（按实际返回顺序）：**

| # | 字段 | 类型 |
|---|------|------|
| 1 | `replica_format` | int |
| 2 | `packing_difficulty` | int |
| 3 | `unpacked_chunk_hash` | string(43) |
| 4 | `unpacked_chunk2_hash` | string(43) |
| 5 | `chunk2_hash` | string(43) |
| 6 | `merkle_rebase_support_threshold` | string(15) |
| 7 | `chunk_hash` | string(43) |
| 8 | `block_time_history_hash` | string(43) |
| 9 | `recall_byte2` | string(15) |
| 10 | `hash_preimage` | string(43) |
| 11 | `recall_byte` | string(15) |
| 12 | `reward` | string(12) |
| 13 | `previous_solution_hash` | string(43) |
| 14 | `partition_number` | int |
| 15 | `nonce_limiter_info` | object(11) |
| 16 | `poa2` | object(5) |
| 17 | `signature` | string(683) |
| 18 | `reward_key` | string(683) |
| 19 | `price_per_gib_minute` | string |
| 20 | `scheduled_price_per_gib_minute` | string |
| 21 | `reward_history_hash` | string(43) |
| 22 | `debt_supply` | string |
| 23 | `kryder_plus_rate_multiplier` | string |
| 24 | `kryder_plus_rate_multiplier_latch` | string |
| 25 | `denomination` | string |
| 26 | `redenomination_height` | int |
| 27 | `double_signing_proof` | object(0) |
| 28 | `previous_cumulative_diff` | string(16) |
| 29 | `usd_to_ar_rate` | array(2) |
| 30 | `scheduled_usd_to_ar_rate` | array(2) |
| 31 | `packing_2_5_threshold` | string |
| 32 | `strict_data_split_threshold` | string(14) |
| 33 | `nonce` | string(2) |
| 34 | `previous_block` | string(64) |
| 35 | `timestamp` | int |
| 36 | `last_retarget` | int |
| 37 | `diff` | string(78) |
| 38 | `height` | int |
| 39 | `hash` | string(43) |
| 40 | `indep_hash` | string(64) |
| 41 | `txs` | array(string) |
| 42 | `tx_root` | string(0\|64) |
| 43 | `wallet_list` | string(64) |
| 44 | `reward_addr` | string(43) |
| 45 | `tags` | array(object) |
| 46 | `reward_pool` | string(18) |
| 47 | `weave_size` | string(15) |
| 48 | `block_size` | string |
| 49 | `cumulative_diff` | string(16) |
| 50 | `hash_list_merkle` | string(64) |
| 51 | `poa` | object(5) |

**关键嵌套子结构：**

- **`poa` / `poa2`** (5 字段): `option`, `tx_path`, `data_path`, `chunk`, `unpacked_chunk` (均为 base64url)
- **`nonce_limiter_info`** (11 字段): `output`, `global_step_number`, `seed`, `next_seed`, `zone_upper_bound`, `next_zone_upper_bound`, `prev_output`, `last_step_checkpoints`, `checkpoints`, `vdf_difficulty`, `next_vdf_difficulty`
- **`double_signing_proof`**: 空对象 `{}`（有事件时才含数据）

### 5.3 `/tx/{id}` 响应

共 **13 字段**，详细 schema 见 `API_SCHEMA_VERIFIED.md`。

```json
{
  "format": 2,
  "id": "base64url(43)",
  "last_tx": "base64url(64)",
  "owner": "string(683, RSA public key)",
  "tags": [{"name": "base64url", "value": "base64url"}],
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

| 字段 | 类型 | 说明 |
|------|------|------|
| `format` | int | 交易格式版本 (1 或 2) |
| `id` | string(43) | 交易 ID |
| `last_tx` | string(64) | 上一笔交易 ID |
| `owner` | string(683) | 所有者 RSA 公钥 |
| `tags` | array | `[{"name":"...","value":"..."}]`，name/value 均为 base64url |
| `target` | string | 目标地址（空字符串 = 无目标） |
| `quantity` | string | 转账金额 (Winston) |
| `data` | string | 交易数据 (base64url，可为空) |
| `data_size` | string | 数据大小 (字节) |
| `data_tree` | array | 数据 Merkle 树 (通常为空数组 `[]`) |
| `data_root` | string(43) | 数据 Merkle 根 |
| `reward` | string | 矿工费 (Winston) |
| `signature` | string(683) | 交易签名 |

> ⚠️ 注意：响应中**没有** `signature_type` 字段。该字段仅出现在交易**提交**格式中，不在查询响应中。

### 5.4 `/price/{bytes}` 响应

- **arweave.net 实际行为**: 纯文本整数 (Winston)，Content-Type: `text/plain; charset=utf-8`。示例: `"1527512677"`
- `/price2/{bytes}` 端点: 始终返回 JSON `{"price": "123456789"}`
- `/v2price/{bytes}` 端点: 新定价算法，返回格式同 `/price2`

### 5.5 `/tx_anchor` 响应

纯文本 Base64URL 编码的区块哈希，例如:
```
a1b2c3d4e5f6g7h8i9j0k1l2m3n4o5p6q7r8s9t0u1v2w3x4y5z6a7b8c9d0
```

### 5.6 `/peers` 响应

```json
["128.241.227.87:5984", "49.12.173.84:1984", "65.21.217.222:1984"]
```

> ⚠️ **arweave.net 实际返回**裸 `ip:port`，**不带 `http://` 前缀**。标准 Erlang 节点源码中返回的是 `http://ip:port`，但 arweave.net 网关层做了去前缀处理。

---

## 八、实现优先级建议

### 第一优先级 (核心查询 — 已大部分实现)
- [x] `/info` — 需补齐字段
- [x] `/height` — 待实现
- [x] `/block/height/{height}` — 已有
- [ ] `/block/hash/{hash}` — **待实现**
- [x] `/tx/{id}` — 已有
- [x] `/tx/{id}/data` — 已有
- [x] `/peers` — 已有
- [x] `/tx_anchor` — 已有

### 第二优先级 (常用查询 — 轻节点可实现)
- [ ] `/time` — 简单，本地时钟
- [ ] `/wallet/{addr}/balance` — 已有
- [ ] `/wallet/{addr}/last_tx` — 转发即可
- [ ] `/price/{bytes}` — 需要定价算法
- [ ] `/tx/{id}/status` — 需转发
- [ ] `/tx/{id}/{field}` — 可转发
- [ ] `/block/current` — 简单重定向
- [ ] `/recent` — 返回本地最近区块

### 第三优先级 (扩展 — 需额外工作)
- [ ] `/inflation/{height}` — 确定性算法
- [ ] `/v2price/{bytes}` — 新定价算法
- [ ] `/tx/pending` — 需转发
- [ ] `/hash_list/{from}/{to}` — 部分可实现
- [ ] `/block/{type}/{id}/{field}` — 部分字段可返回

### 不需要实现 (全节点 / 矿工专属)
- 所有 POST 提交类 (除 `/tx` 代理转发)
- 所有 VDF / CM / 挖矿类
- 所有数据同步 / chunk 存储类
- 所有 wallet_list 类
- `/queue`, `/sync_buckets`, `/footprints`, `/data_roots`, `/data_sync_record`
