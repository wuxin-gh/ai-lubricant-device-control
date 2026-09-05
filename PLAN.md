# Device Control — 计划文档 (v0)

> 本文件是交接文档：一个全新的会话/窗口应能只读本文件就接手实施。
> 决策背景来自与 ai-lubricant 团队的讨论，结论已定，无需重新论证。

## 0. 一句话定位

一个**独立开源**的手机远程控制工具：设备端装一个 App，主动拨号连一个
（用户自部署的）服务端，服务端下发"读屏/点击/输入"等指令，设备端执行并回报。
用**自由协议**（非 MCP）以获得低延迟体验与 NAT 穿透便利性。

## 1. 硬性边界（这些是"商务风险归零"的前提，不可妥协）

- **独立仓库**，不进 ai-lubricant 主仓，不与 `monkeycode-ai.com` 共用任何域名/服务端/证书。
- **不绑公司主体**：用个人或独立身份在 GitHub 开源发布。
- **默认不连任何生产服务**：仓库内**不预置**指向任何托管服务端的默认地址；服务端地址一律用户自填。
- **不分发连第三方后端的预编译包**：release 只含"自部署"形态。
- **定位为工具**（类比 scrcpy / ARC-MCP），非"我们产品的功能"。README 明确：自部署、仅限操作使用者自有设备。
- **数据控制者是部署者**：项目本身不收集、不回传、不托管任何用户数据。

> 只要以上任一条被破坏（尤其"默认指向我们的服务端"），数据控制者身份就回到发布方，
> 前述法律/合规/账号连坐风险会立即重新出现。实施时任何 PR 触碰这些边界都应被拒。

## 2. 代码来源与许可证

- **主要复用**：ARC-MCP (`danielealbano/android-remote-control-mcp`, **MIT**) 的
  - `services/accessibility/*` — 无障碍读屏树 + 动作执行 + 元素查找
  - `services/screencapture/*` — 截图 + 标注
  - `privacy/*` — 端上敏感信息脱敏（出网前打码）
  - 保留其 MIT 版权声明 / NOTICE 即可商用。
- **丢弃不搬**：`mcp/*`（Ktor MCP server + JSON-RPC 工具注册）、`mcp/oauth/*`、
  `services/tunnel/*`（Cloudflare/ngrok）、独立 App 的 `ui/*`（保留最小授权/状态界面）。
- **参考不抄**：mobile-mcp (`mobile-next/mobile-mcp`) 的 `src/robot.ts` —— 平台无关能力抽象，
  用作"命令词汇表"设计参考；其 iOS/农场路径（go-ios + WebDriverAgent）留待后续档位。
- **协议范式参考**：ai-lubricant 的节点客户端 `nodes/common/agent/client.go` ——
  "设备主动拨号 + 心跳 + 指数退避重连"的实现模式，照搬思路（但用 WS+JSON，不用 connect-rpc/h2c）。

## 3. 架构

```
┌──────────────┐   WS(JSON) 长连接    ┌──────────────────┐
│  设备端 App   │ ───────拨号───────▶ │  服务端(自部署)   │
│ (Android 优先)│ ◀──指令下发/心跳──── │  控制端连它来驱动 │
│ 无障碍+截图   │                     └──────────────────┘
└──────────────┘   设备始终是主动方，穿透 NAT
```

- **传输**：WebSocket + JSON 信封。选 WS 而非 connect-rpc/h2c 的原因：Kotlin/Swift/TS
  三端 WS+JSON 都便宜，h2c connect 在 Kotlin/Swift 无好实现。
- **信封**：借 ai-lubricant `tasks/control` 语义 —— `call / call-response`，`request_id` 配对。
- **方向**：设备端拨号（解决网络便利性）；服务端可随时下发指令；心跳 + 指数退避重连。
- **能力协商**：设备注册时声明支持的命令集（借 `NodeRegister.capabilities` 思路），
  服务端只下发设备声明支持的命令 —— 三种实现（Android 全量 / iOS 只读 / 农场）能力不齐也不炸。

## 4. 协议 spec v0（已定稿：`spec/protocol-v0.md`，本节仅存骨架作为背景）

信封（JSON over WS 文本帧）：
```
设备→服务端: { "type":"register", "device_id", "auth", "capabilities":[...] }
             { "type":"heartbeat", "device_id" }
             { "type":"call-response", "request_id", "ok":bool, "data"|"error" }
             { "type":"event", "kind", "data" }        // 主动上报(可选)
服务端→设备: { "type":"registered", "device_id" }
             { "type":"call", "request_id", "cmd", "args":{...} }
```

命令词汇表（对着 ARC-MCP 14 类裁剪，v0 先做最小闭环）：
- `get_screen_state` — 返回压缩 UI 树（TSV），可选带标注截图
- `tap` / `long_press` / `double_tap` — 按坐标或 node_id
- `swipe` / `scroll`
- `type_text` / `press_key` / `dismiss_keyboard`
- `press_back` / `press_home` / `press_recents`
- `open_app` / `list_apps`
- （后续）文件、剪贴板、通知、相机等

配对与鉴权（设备不走用户 Cookie，可能不在用户手上）：
- 服务端 onboard 出一个**长期凭据**（`device_id` + secret），设备持久化后自拨号重连。
- v0 可用简单长期 token；若要更强，参考节点的 TOTP 方案（`nodes/common/auth/totp`）。
- 配对入口：服务端生成配对码 → 设备端扫码/输入 → 换取长期凭据并落盘（0600）。

## 5. 仓库结构（建议）

```
device-control/
├── PLAN.md                 # 本文件
├── LICENSE                 # MIT（含 ARC-MCP 的原始版权声明）
├── NOTICE                  # 标注复用来源
├── README.md               # 强调：自部署 / 仅限自有设备 / 不含默认服务端地址
├── spec/
│   └── protocol-v0.md      # 协议 spec 定稿
├── android/                # 纯 Kotlin 原生工程（非 Expo/RN）
│   ├── core/               # 从 ARC-MCP 搬: accessibility + screencapture + privacy
│   └── app/                # ForegroundService + WS 客户端 + 最小授权/状态 UI
└── server/                 # 最小参考实现(自部署), 用户可替换
```

## 6. 实施里程碑

- **M0 — 协议定稿**：✅ 已完成，见 `spec/protocol-v0.md`（§6 待定问题全部解决，
  决策记录在该文档 §15）。
- **M1 — Android core**：✅ 已完成，见 `android/core/`。从 ARC-MCP 抽 `accessibility/`+`screencapture/`
  成 `core/` library（29 个 main 源 + 14 个测试，261 测试全绿）。MCP/OAuth/tunnel/UI 已剥；
  `McpToolException`→`CoreException`、`ToolCallIndicator`→`CommandIndicator`，服务类的 Hilt
  `@EntryPoint` 换成静态 `nodeCacheProvider` hook（`core/` 不带 DI 运行时）。
  **privacy/ 按设计推迟到 M4**：上游是 JVM 模块（ONNX runtime + 151MB NER 模型 + 60 文件），
  不进 Android `core/`；端上脱敏接线在 M4 决定形态。
- **M2 — Android app**：✅ 已完成，见 `android/app/`。ForegroundService（specialUse）持 OkHttp WS 长连接，
  实现 register/heartbeat/full-jitter 重连（§7）/close-code 分类（4003 擦凭据、4004 停、其余退避）；
  `CommandDispatcher` 处理 request_id 去重 + in-flight ≤8 + timeout 预算 + 异常→错误码映射；
  16 个命令 handler（get_screen_state 走新增的 core `ScreenReader`；node_id 优先 a11y 动作；
  scroll_to_node 组合 5 次/300ms；press_key 映射 §8.6 枚举；open_app/list_apps 走 PackageManager）。
  最小 UI：无障碍状态 + 空（默认）地址框 + 配对码 + 连接/断开/解除配对。`allowBackup=false`、
  凭据 app 私有文件 0600。core + app 共 334 测试全绿，`:app:assembleDebug` 产出 14MB APK。
  **真机端到端验证仍待做**（本机是 VM，跑不了模拟器）：服务端起 → 配对 → `get_screen_state`
  拿 TSV → `tap` 真机响应 → `DELETE` 触发 4003 擦凭据。
- **M3 — 参考服务端**：✅ 已完成，见 `server/`（Go，单二进制自部署）。配对/注册/下发/收结果
  全通，11 个端到端测试覆盖握手、能力门禁、结构化错误、重连挤占、吊销即时断连。
  详见 `server/README.md`。注意：默认只听 127.0.0.1 且不做 TLS，对外暴露需自备反代。
- **M4 — 打磨**：能力协商、端上脱敏接线、审计日志、异常熔断（只能控自己 onboard 的设备）。
- **后续档位（不在 v0）**：iOS 只读查看（ReplayKit 推屏）；设备农场驱动（照 mobile-mcp 用 adb+go-ios/WDA）。

## 7. 已排除的方案（不要回头再议）

- ❌ 把能力做成 ai-lubricant 主 App 的一个 flavor —— Expo 转 bare 维护税高，且上架/账号风险贴着主体。
- ❌ 复用节点 connect-rpc/h2c 协议 —— 语义不符（全是 coding session），Kotlin/Swift 无好 h2c 实现。
- ❌ 直接当 MCP 用 —— 体验差（每轮 RPC 往返 + schema 进 prompt），且要用户自己会装+配 MCP。
- ❌ iOS 上做"代操作任意 App" —— 非越狱平台级封死；iOS 只能只读查看 / 农场驱动。
- ❌ 由我方托管服务端并回传数据 —— 会使我方成为数据控制者，触发全部合规风险。

## 8. 新窗口接手指引

1. 先只读本文件即可掌握全部决策，无需重新论证方向。
2. 工作目录：`d:\code\device-control`（已建，独立于 ai-lubricant 主仓）。
3. 第一步做 **M0**：把 §4 骨架细化成 `spec/protocol-v0.md` 定稿。
4. 需要看 ARC-MCP 源码时，文件路径见 §2；仓库 `danielealbano/android-remote-control-mcp`。
5. 严守 §1 硬性边界 —— 任何"默认连生产服务端 / 绑公司主体"的改动都要拒绝。

