# yggdrasil peers `fakehttp=` URI 参数支持设计

## 背景与目标

在现有 peer URI query 参数机制上新增 `fakehttp=<hostname>` 支持，参考 FakeHTTP 的行为思路，但保持对当前版本的高度兼容：

- 未配置 `fakehttp` 时，行为与现版本完全一致。
- 配置后仅作为“连接前注入”能力，不改变 Yggdrasil 真实握手与连接生命周期。
- 优先 Linux raw socket 低 TTL 注包；权限不足时自动降级，不中断真实连接。

## 已确认约束

- 参数语义：`fakehttp=<hostname>`（单值）。
- 协议范围：允许出现在所有 peer URI 上；仅 TCP 家族生效。
- 非 TCP（如 `quic://`、`unix://`）：忽略并 `warn` 后继续。
- 权限不足：`warn` 后降级为“短连接发送极简 HTTP GET 再关闭”，然后继续真实连接。
- 注入触发：每次连接尝试都执行（含重连）。
- `Listen` 中出现 `fakehttp`：忽略并 `warn`，listener 正常启动。
- `socks://` / `sockstls://`：注入目标为代理地址。
- 去重策略：`fakehttp` 不参与 peer 唯一性判定（保持现有 query 去重行为）。
- 默认 TTL：3。

## 架构设计

### 1. 参数解析层（增量）

在现有 query 解析流程中新增 `fakehttp` 读取，写入 `linkOptions` 新字段（如 `fakeHTTPHost string`）。

- 非法值采用降级策略：忽略参数并记录 `warn`。
- 不修改现有 URI 归一化与去重键规则，保障兼容性。

### 2. 注入调度层（连接前钩子）

在每次 `connect()` 真正拨号前插入 `maybeInjectFakeHTTP()`：

- 协议分流：
  - `tcp/tls/ws/wss/socks/sockstls`：尝试注入。
  - 其他协议：`warn` 并跳过。
- 目标地址选择：
  - 普通协议：peer host:port。
  - SOCKS 协议：proxy host:port。
- 接口约束：若来自 `InterfacePeers`，注入必须绑定同一 `sintf`；无法绑定时进入降级路径并 `warn`。

### 3. 注入执行层（两级）

#### Raw 注入（首选）

Linux-only，使用 raw socket 构造并发送单次低 TTL（3）HTTP 伪装包。

- 需要 `CAP_NET_RAW/root`。
- 成功仅 `debug`。

#### Fallback 注入（权限不足或 raw 失败）

建立短 TCP 连接，发送极简请求后立即关闭：

```http
GET / HTTP/1.1

Host: <fakehttp-host>

Connection: close



```

随后继续真实 Ygg 连接流程。

## 数据流

1. 解析 peer URI -> 生成 `linkOptions(fakeHTTPHost)`。
2. 进入连接尝试（每轮重连都会进入）。
3. 调用 `maybeInjectFakeHTTP()`：协议检查 -> raw 尝试 -> fallback（必要时）。
4. 不论注入结果，继续原有 `dial + handshake + handler`。

## 错误处理与日志

- 成功注入：`debug`。
- 降级、忽略、失败：`warn`（含 peer、模式、原因）。
- 注入错误默认不写入 `LastError` 主语义，避免污染“真实连接失败”诊断。

## 兼容性策略

- 默认路径零影响：未配置时无行为变化。
- 跨平台构建不破坏：Linux 有 raw 实现；其他平台提供 no-op/raw-not-supported 分支，必要时仅 fallback 或跳过。
- 不新增强制配置项，不改 admin 数据结构，不改现有 backoff 节奏。

## 测试与验收

### 单元测试

- `fakehttp` 合法/空/非法值解析。
- 非 TCP 跳过行为。
- SOCKS 目标地址选择。
- `Listen` 中参数忽略行为。

### 逻辑测试（mock）

- raw 成功路径。
- raw 权限失败 -> fallback 成功路径。
- raw 与 fallback 都失败，但真实连接仍继续。
- 重连多轮注入调用计数。

### 回归测试

- 现有核心连接测试在未配置 `fakehttp` 时结果不变。
- 配置 `fakehttp` 后连接可建立，不破坏握手与重连。

---

该设计为最小侵入实现，优先保证向后兼容与运行稳定性，再逐步扩展更高级的可调参数（如 TTL/path/header 等）。
