# Arweave Gateway 专用 API 规范调研

> 调研日期: 2026-05-07  
> 信息来源: [arweave-source](https://github.com/ArweaveTeam/arweave) (Erlang 全节点源码),  
> [arweave-light](https://github.com/arweave-light) (Go 轻节点),  
> [Path Manifest Schema](https://github.com/ArweaveTeam/arweave/blob/master/doc/path-manifest-schema.md),  
> [ar.io Network](https://ar.io) (社区网关生态)  
> 目标: 记录 arweave.net 网关**独有**的端点与行为（与标准节点 API 不重叠的部分）。  
> ⚠️ 标准节点 API 请参考 [`API_SURVEY.md`](./API_SURVEY.md) 和 [`API_SCHEMA_VERIFIED.md`](./API_SCHEMA_VERIFIED.md)。

---

## 概述

Arweave Gateway 是在标准 Arweave 节点 HTTP API 之上构建的**内容分发层**。Gateway 独有的能力：

- 面向浏览器的内容渲染 (`/{txid}`, `/{txid}/{path}`)
- ArNS 域名解析 (`/{name}` → `/{txid}`)
- 子域名路由 (`{subdomain}.arweave.net/{path}`)
- GraphQL 查询接口
- CDN 缓存头 (ETag, Cache-Control, 304)
- 交易黑名单过滤
- Manifest 路径解析

---

## 一、交易数据渲染 (Content Rendering)

这是网关最核心的增值能力。标准节点只返回原始数据，网关负责按 Content-Type 渲染。

### 1.1 `GET /{txid}` — 裸交易 ID 渲染

| 属性 | 值 |
|------|-----|
| **网关特有?** | ✅ **是。标准节点不处理裸 `/{txid}`**（标准节点只处理 `/{txid}.{ext}` 带扩展名） |
| **行为** | 根据交易 tags 中的 `Content-Type` 设置 HTTP `Content-Type` 响应头，返回交易数据 |
| **Content-Type 提取逻辑** | 见下方 §1.4 |
| **缺失 Content-Type 时** | 默认 `text/html` |
| **无效 Content-Type 时** | 返回 `421` 状态码 |
| **轻节点可实现?** | ✅ 完全可实现 |

**源码依据** (ar_http_iface_middleware.erl):
```erlang
%% 标准节点: 有扩展名才处理 (网关额外处理裸 txid)
handle(<<"GET">>, [<<Hash:43/binary, MaybeExt/binary>>], Req, Pid) ->
    handle(<<"GET">>, [<<"tx">>, Hash, <<"data.", MaybeExt/binary>>], Req, Pid);
```
标准节点**没有**裸 `/{txid}` (无扩展名) 的 handler。这是网关添加的。

### 1.2 `GET /{txid}.{ext}` — 带扩展名访问

| 属性 | 值 |
|------|-----|
| **网关特有?** | ⚠️ 标准节点已支持，但网关有增强 |
| **标准节点行为** | 将 `/{txid}.{ext}` 重写为 `/tx/{id}/data.{ext}` |
| **网关增强** | 可能根据扩展名强制覆盖 Content-Type (如 `.html` → `text/html`) |
| **轻节点可实现?** | ✅ 标准节点已有，轻节点可照做 |

### 1.3 `GET /{txid}/{path}` — Manifest 路径解析

| 属性 | 值 |
|------|-----|
| **网关特有?** | ✅ **是。标准节点完全不支持路径解析** |
| **行为** | 若交易 Content-Type tag 为 `application/x.arweave-manifest+json`，解析 manifest JSON，根据 `paths` 映射将子路径路由到子交易 ID |
| **Manifest Schema** | 见 [path-manifest-schema.md](https://github.com/ArweaveTeam/arweave/blob/master/doc/path-manifest-schema.md) |
| **Fallback** | 若路径不存在于 manifest，返回 404 |
| **`index` 行为** | 若 manifest 有 `index.path` 字段，访问 `/{txid}` 自动跳转到该路径 |
| **无 `index` 时** | 网关应该返回路径列表 (directory listing) |
| **嵌套 Manifest** | 支持：子路径指向的交易也可以是 manifest 类型，递归解析 |
| **轻节点可实现?** | ✅ 完全可实现 |

**Manifest 示例**:
```json
{
  "manifest": "arweave/paths",
  "version": "0.1.0",
  "index": {
    "path": "index.html"
  },
  "paths": {
    "index.html": { "id": "cG7Hdi_iTQPoEYgQJFqJ8NMpN4KoZ-vH_j7pG4iP7NI" },
    "styles/main.css": { "id": "fZ4d7bkCAUiXSfo3zFsPiQvpLVKVtXUKB6kiLNt2XVQ" },
    "images/logo.png": { "id": "QYWh-QsozsYu2wor0ZygI5Zoa_fRYFc8_X1RkYmw_fU" }
  }
}
```

> `GET /{txid}/styles/main.css` → 获取子交易 `fZ4d7bk...` 的数据，Content-Type 来自子交易 tags

### 1.4 Content-Type 提取机制

| 属性 | 值 |
|------|-----|
| **来源** | 交易 tags 中 `name` 为 `Content-Type` 的 tag |
| **大小写敏感?** | ⚠️ **是。** 标准节点代码 `lists:keyfind(<<"Content-Type">>, 1, Tags)` 精确匹配 |
| **有效性验证** | `ar_http_util:is_valid_content_type/1` — 只允许 ASCII 可打印字符 (`^[ -~]*$`) |
| **返回值** | `{valid, ContentType}` — 有效; `none` — 未设置; `invalid` — 无效 (返回 421) |
| **轻节点可实现?** | ✅ 完全可实现 |

**源码** (ar_http_util.erl):
```erlang
get_tx_content_type(#tx { tags = Tags }) ->
    case lists:keyfind(<<"Content-Type">>, 1, Tags) of
        {<<"Content-Type">>, ContentType} ->
            case is_valid_content_type(ContentType) of
                true -> {valid, ContentType};
                false -> invalid
            end;
        false -> none
    end.
```

---

## 二、Arweave Name System (ArNS)

| 属性 | 值 |
|------|-----|
| **网关特有?** | ✅ **是。标准节点不支持 ArNS** |
| **归属** | [ar.io Network](https://ar.io) 提供的去中心化域名系统 |
| **原理** | ArNS 是一个 **Arweave 上的 SmartWeave 合约**，将域名映射到交易 ID |
| **网关角色** | 网关缓存 ArNS 合约状态，将 `GET /{name}` 解析为对应的 Arweave 交易 ID |
| **解析流程** | `GET /myapp` → 查 ArNS 注册表 → 获得 txid `abc123...` → 按 `GET /{txid}` 逻辑渲染 |

### 2.1 `GET /{name}` — 域名解析

| 属性 | 值 |
|------|-----|
| **URL 格式** | `https://arweave.net/{name}` 或 `https://{name}.arweave.net` (通过子域名) |
| **解析目标** | ArNS 域名 → Arweave Transaction ID |
| **TTL/缓存** | 网关缓存解析结果 (通常 ~30 分钟) |
| **未注册域名** | 返回 404 |
| **过期域名** | 返回 404 或显示过期页面 |
| **轻节点可实现?** | ⚠️ 需要读取 ArNS 合约状态 (SmartWeave)，可实现但较复杂 |

### 2.2 ArNS 子域名

| 属性 | 值 |
|------|-----|
| **格式** | `{subdomain}_{parent_name}` (如 `blog_myapp`) |
| **解析** | ArNS 合约支持子域名记录，类似 DNS |
| **网关行为** | 与主域名相同，解析后渲染 |

---

## 三、子域名支持 (Subdomain Routing)

| 属性 | 值 |
|------|-----|
| **网关特有?** | ✅ **是。标准节点不支持** |
| **实现位置** | 通常在网关的反向代理层 (如 Nginx / Cloudflare Workers) |

### 3.1 `{subdomain}.arweave.net/{path}` — 子域名路由

| 属性 | 值 |
|------|-----|
| **原理** | DNS 层面将 `*.arweave.net` 指向网关 IP；网关根据 Host header 提取子域名 |
| **子域名到 TxID 映射** | 两种方式: <br/>1. **ArNS**: 子域名通过 ArNS 合约的 undername 记录解析 <br/>2. **DNS TXT 记录**: `arweave-tx-id=abc123...` |
| **路径处理** | 子域名解析到 txid 后，剩余 `/path` 通过 manifest 路径解析 |
| **示例** | `https://myapp.arweave.net/about` → DNS/ArNS 解析 `myapp` → txid `abc123...` → manifest 查找 `about` 路径 |
| **轻节点可实现?** | ⚠️ 需要 DNS 配置 + ArNS 合约集成 |

---

## 四、GraphQL 网关扩展

| 属性 | 值 |
|------|-----|
| **网关特有?** | ✅ **是。标准节点源码中不包含 GraphQL handler** |
| **实现** | 由独立的 ardb / arweave-graphql 服务提供，通过 `/graphql` 端点暴露 |
| **路由** | 网关将 `/graphql` 请求代理到内部 GraphQL 服务 |

### 4.1 `POST /graphql` — GraphQL 查询

| 属性 | 值 |
|------|-----|
| **请求格式** | `{"query": "{ transactions(first: 10) { edges { node { id } } } }" }` |
| **轻节点可实现?** | ⚠️ 需要 ardb 索引数据库 |

### 4.2 网关特有的 GraphQL 字段

| GraphQL 字段 | 描述 | 对应标准节点 API |
|---|---|---|
| `arPrice` | 当前 AR 代币的 USD 价格 | 无对应端点 (来自外部预言机) |
| `gateway` | 当前网关主机名 | — |
| `transactions` | 分页交易查询 (支持 tag 过滤、时间范围) | 无对应端点 (需要索引) |
| `blocks` | 分页区块查询 | 标准节点: `/block/height/{h}` 单块查询 |

### 4.3 ArQL (已废弃)

| 属性 | 值 |
|------|-----|
| **端点** | `POST /arql` |
| **状态** | 已废弃，由 GraphQL 替代 |

---

## 五、缓存/CDN 相关

| 属性 | 值 |
|------|-----|
| **网关特有?** | ✅ **是。标准节点不设置任何缓存头** |
| **标准节点源码** | `ar_http_iface_middleware.erl` 中**没有任何** `Cache-Control`、`ETag`、`If-None-Match`、`304` 相关的处理代码 |

### 5.1 网关缓存行为

| 缓存机制 | 描述 | 实现情况 |
|---|---|---|
| **Cache-Control** | `public, max-age=31536000, immutable` (数据永久存储，不可变) | ✅ 网关实现 |
| **ETag** | 基于交易 ID (内容寻址，天然防篡改) | ✅ 网关实现 |
| **304 Not Modified** | 支持 `If-None-Match` 条件请求 | ✅ 网关实现 |
| **Last-Modified** | 基于区块时间戳 | ✅ 网关可能实现 |
| **CDN** | arweave.net 背后有 Cloudflare CDN | ✅ 生产环境 |

### 5.2 轻节点实现建议

| 特性 | 可实现性 | 说明 |
|------|----------|------|
| `Cache-Control` | ✅ | 直接返回 `immutable`，数据不可变 |
| `ETag` | ✅ | 用交易 ID 作为 ETag |
| `304` | ✅ | 比较 `If-None-Match` 请求头 |
| `Last-Modified` | ⚠️ | 需查询区块时间戳 |

---

## 六、交易黑名单 (Gateway 特有)

| 属性 | 值 |
|------|-----|
| **端点** | `GET /is_tx_blacklisted/{txid}` |
| **标准节点支持?** | ✅ 标准节点**已有**此端点 |
| **网关行为差异** | 网关可能在渲染 `/{txid}` 时**直接拒绝返回**已列入黑名单的内容 (421/451 Unavailable For Legal Reasons) |
| **配置** | `transaction_blacklist_files`, `transaction_blacklist_urls` |

---

## 七、网关特有 HTTP 响应头

| Header | 描述 | 来源 |
|--------|------|------|
| `Cache-Control: public, max-age=31536000, immutable` | 永久缓存 | 网关添加 |
| `ETag: "{txid}"` | 基于交易 ID | 网关添加 |
| `Access-Control-Allow-Origin: *` | CORS 支持 | 标准节点已添加 (所有响应) |
| `X-Arweave-Tx-Id` | 返回实际渲染的 TxID | 部分网关添加 |

---

## 八、实现优先级建议 (轻节点视角)

### 第一优先级 — 核心网关功能 (可直接实现)
- [ ] `GET /{txid}` — 裸交易 ID 数据渲染 (Content-Type from tags)
- [ ] `GET /{txid}.{ext}` — 带扩展名渲染
- [ ] `GET /{txid}/{path}` — Manifest 路径解析
- [ ] `Cache-Control: immutable` 响应头
- [ ] `ETag` 支持
- [ ] 304 Not Modified 支持

### 第二优先级 — 增强网关功能
- [ ] 无 Content-Type tag 时的内容嗅探 (MIME type detection)
- [ ] Manifest `index.path` 处理
- [ ] 嵌套 Manifest 递归解析
- [ ] 目录列表 (无 index 时的 paths 展示)
- [ ] `Last-Modified` 响应头

### 第三优先级 — 需要外部依赖
- [ ] ArNS 域名解析 (需读取 ArNS SmartWeave 合约)
- [ ] GraphQL 端点 (需 ardb 或类似索引服务)
- [ ] 子域名路由 (需 DNS 配置)

---

## 九、关键源码引用

### 标准节点 HTTP 路由核心
- **文件**: `apps/arweave/src/ar_http_iface_middleware.erl` (~3300 行)
- **路由入口**: `handle(Method, SplitPath, Req, Pid)`
- **交易渲染**: `serve_tx_data/2`, `serve_tx_html_data/2`, `serve_format_2_html_data/3`

### Content-Type 提取
- **文件**: `apps/arweave/src/ar_http_util.erl`
- **函数**: `get_tx_content_type/1`
- **验证**: `is_valid_content_type/1` (ASCII 可打印字符正则)

### Manifest Schema
- **文件**: `doc/path-manifest-schema.md`
- **Content-Type**: `application/x.arweave-manifest+json`

### 黑名单
- **文件**: `apps/arweave/src/ar_tx_blacklist.erl`
- **端点**: `GET /is_tx_blacklisted/{txid}`

---

## 十、外部参考

| 资源 | URL |
|------|-----|
| Arweave HTTP API 文档 | https://docs.arweave.org/developers/server/http-api |
| Path Manifest Schema | https://github.com/ArweaveTeam/arweave/blob/master/doc/path-manifest-schema.md |
| ar.io Network (社区网关) | https://ar.io |
| ArNS 文档 | https://ar.io/arns |
| ardb (GraphQL 索引) | https://github.com/textury/ardb |
| Arweave Gateway 源码 (参考实现) | https://github.com/ArweaveTeam/arweave-gateway |
| Go 轻节点实现 | https://github.com/arweave-light (本项目) |
