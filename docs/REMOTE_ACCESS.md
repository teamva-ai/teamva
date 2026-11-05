# 远程访问

## 使用

普通浏览器和 Wails 共用「设置 → 远程访问」。本机和局域网只用一个端口，本机 localhost / 127.0.0.1 访问免访问密码。首次使用默认关闭局域网；设置密码、打开
「允许远程访问」并保存后，其他设备即可打开页面显示的 `http://IP:端口`。
工作台自有 `/remote/login` 页面只要求访问密码，无用户名，不使用浏览器原生 Basic 登录框。
登录成功后跳回原先访问的站内页面；密码错误与限流直接在登录页提示。

登录页提供默认不勾选的「信任此设备」。勾选后服务端长期记住，不设置到期时间，仅实际修改访问密码时撤销信任。重启、关闭再开启远程访问和同 IP 更换服务端口不撤销信任。
已有有效登录或信任凭据时，即使直接打开登录页书签，也自动跳回工作台。清除浏览器 Cookie、更换浏览器或 IP 后可能需要重新登录。
请仅在私人设备上勾选。Chrome 的 Cookie 存储上限是 400 天：应用在每次已认证访问时重新签发同一 HttpOnly Cookie 以续期，这不是服务器的设备信任期限；长期完全不访问、浏览器清理或用户删除站点数据仍可能使浏览器丢失凭据。不能用 JS 可读的长期令牌来绕过此限制。

- 不再配置远程端口。CLI 启动脚本统一默认 `41720`；`TEAMVA_LOCAL_HTTP_ADDR=127.0.0.1:<端口>` 可在启动时选择 `1024–65535`。`0` 复用保存的端口，首次自动分配并保存。Wails 首次自动分配，重启复用。
- 密码至少 8 字符，最多 72 个 UTF-8 字节；不允许纯空白或控制字符。
- 密码框不会回填已保存密码；留空表示不修改。
- 设置即时生效、重启后保留；不需要重启 Wails。
- 停用或修改密码会断开远程连接，包括 SSE；本机窗口和正在执行的任务不因此停止。
- 局域网绑定失败不阻止本机启动；恢复网络或释放占用后重新开启即可。本机端口被占用时明确拒绝启动，不偷偷另选端口。
- 仅支持显示出来的 LAN IPv4 地址直接访问；不绑定 wildcard，也不绑定 point-to-point VPN 网卡。更换网卡或 IP 后关闭再开启，以重新绑定当前地址。

`pnpm start:lan` 是 CLI 首次配置入口，与 `pnpm start:browser` 共用服务端口 41720；无密码时只开放本机入口。
`TEAMVA_LOCAL_ACCESS_TOKEN` 可作为自动化首次密码种子，已有开关和密码优先；
该环境变量在 Provider 初始化前移除，不输出其值。

## 边界与实现

`server/app/network_access.go` 管理具体局域网 IPv4 地址上的独立 `tcp4` sockets，与始终保留的 loopback listener
使用同一端口号，共用 HTTP 路由、SPA、SQLite 和 Runtime。设置更新串行执行，先预留所有 LAN sockets、再持久化、最后启用；
任何绑定或保存失败保留现有服务，所有临时 sockets 被关闭。停用时关闭全部 LAN sockets 和远程长连接，不重绑或关闭本机 socket。

本机 `app_metadata.network_access` 包含 enabled、port、bcrypt 哈希，以及可信设备的随机凭据摘要和 Host，不保存可信设备到期时间。HTTP API 只返回
external_access、external_urls、port、password_set、can_manage 和 listen_error，不返回密码/哈希。
哈希不放入 Runtime 协议、Provider 环境、日志、运行状态文件或诊断导出。

局域网 socket 上的业务请求始终先验证远程会话，不根据 Host、Forwarded 或 TCP peer 的伪装切换为本机身份。仅登录页、favicon 与构建静态资源可公开读取。业务层收到的请求携带服务端
context 标记，Authorization 已移除。远程客户端可以使用工作台，但不能更新此安全设置，也不能
使用 Wails 私有 bootstrap。IP Host 检查、防 DNS rebinding、同源和 CSRF 保护仍保留，
Forwarded/X-Forwarded-For 不作为本机身份依据。

bcrypt 验证用于登录（固定容量限流和最多两个并行计算）及本机设置密码时判断是否实际改动，不在普通资源或 SSE 访问中验证密码。
`POST /v1/auth/remote-session` 接收 password 和可选 trust_device（默认 false），校验同源 Origin、JSON 和 Fetch Metadata，拒绝未知字段。
成功后生成 256 位随机会话，令牌只通过 host-only、HttpOnly、SameSite=Strict 的端口专属 Cookie 交付，
普通登录仅在内存中保存令牌摘要，12 小时绝对有效期，服务重启后失效。
显式勾选信任时只持久化随机凭据摘要与 Host，启动时恢复为没有 deadline 的可信会话；不保存密码或原始令牌。
普通与可信会话合计最多 256 个。普通过期会话会清理；可信记录不按时间自动清理，可通过实际改密码撤销全部信任。
可信记录与网络设置保存在同一元数据行，仅实际改密码原子撤销。保存相同密码、关闭/开启或更换端口保留信任；停用会清理普通会话、断开所有远程连接且不接受访问。
普通凭据仅接受当前端口 Cookie 名和完整 Host；可信凭据允许旧端口 Cookie 名，但必须由当前服务的摘要表识别、IP 相同，不能跨服务/跨 IP 重放。
登录保存失败时不替换原 Cookie 或内存登录；已信任浏览器重新登录会轮换凭据并保留信任，即使本次没有再次勾选。
仅普通长连接有会话到期 deadline；可信长连接仍在停用或改密码时断开。普通业务会话 Cookie 单独存在，不能绕过远程入口验证。

未登录的页面导航跳转登录页，API/SSE 请求返回 `remote_auth_required` JSON 401，而不是 HTML 或
WWW-Authenticate。前端在 API 被拒绝、窗口重新获得焦点及每 15 秒会话检查时恢复登录页面。
登录成功的跳转目标经过站内地址校验，拒绝外部 URL、协议相对 URL、反斜杠和认证端点循环。

**HTTP 不提供传输加密。** 仅用于可信 LAN/VPN，不做公网端口转发，不用于公共 Wi-Fi。
登录者具有同一本机工作台的业务操作能力，可能运行命令；这不是多用户隔离系统。

## 长期设备信任验证（2026-09-16）

- 服务端全量、desktop/protocol 测试通过；server/app 的 `go test -race` 和 server `go vet` 通过。
- 覆盖无持久化到期字段、可信请求无过期 deadline、Cookie 续期、跨重启、停用再开启、停用状态重启、同 IP 换端口及换回、重复保存相同密码、登录凭据轮换仍保留信任、实际改密码撤销、写失败和并发登录。另验证另一服务即使同 IP/同密码也不能接受该凭据，普通登录仍保持 12 小时期限。
- Web 227 项测试、Svelte 检查和生产构建通过。真实 Chrome 1280px/390px 两项 `@remote` 流程通过：长期信任文案、400 天浏览器 Cookie、停用再开启后直接访问、改密码后拒绝旧信任。
- 浏览器验收使用仓库外 `teamva-permanent-trust.cnEb18/` 隔离目录；未保存 trace/HAR，测试密码不输出。未操作真实 Wails、未调用真实 Provider 模型或发布制品。
- OpenAPI 校验通过，保留既有 health 4XX 与 bootstrap 2XX 两条警告；`git diff --check` 通过。此改动不改变 SQLite 结构基线，不需要清空业务数据。

## 历史单端口验证（2026-09-15，长期信任调整前）

- `pnpm verify:web`：37 个文件、224 项测试通过；Svelte 检查零错误零警告，生产构建通过（保留已有的大 chunk 提示）。
- `pnpm test:server`、`pnpm test:protocol`、`pnpm test:desktop` 通过；server/app、server/internal/httpapi 的 `go test -race` 通过；server/desktop 的 `go vet` 通过。
- 后端覆盖同端口本机免密与 LAN 密码边界、伪造 Host/Forwarded 不免密、localhost 规范跳转、移除 port 写入字段、端口重启复用、启动换端口撤销信任、多 socket 部分失败回滚、保存失败释放 socket、无网卡失败、占用解除后同端口重试，以及原有密码、SSE、CSRF、信任设备测试。
- 真实 Chrome：`@remote` 两项通过（1280px、390px），检查同端口的本机/局域网入口、远程密码登录、改密码撤销、关闭 LAN 后本机继续访问、无第二个端口输入框。`test:browser:smoke` 8 项通过、1 项跳过（验收服务禁用 Runtime，未配置真实 Agent，因此跳过图对话拖动/发送）。
- Chrome 验收使用新建隔离数据目录和 41720 端口，测试结束关闭服务；密码仅在内存生成，未保存 trace/HAR。截图在密码框清空后保存于仓库外 `teamva-single-port-check.35enT0/` 验收目录。
- ShellCheck、浏览器脚本语法、OpenAPI lint、`git diff --check` 通过；OpenAPI 保留已有的 health 4XX 与 bootstrap 2XX 两条警告。
- 未执行真实 Wails 窗口验收、真实 Codex/OpenCode 模型调用或公开制品发布。

## 历史验证记录（2026-09-15，单端口调整前）

以下为双端口实现的历史证据，不作为本次单端口架构的验证结果。当前单端口测试以代码和本次执行记录为准。

- 自动化：server 全量测试和 race、desktop 测试和 race、server/desktop vet、protocol 测试通过。
- Web：37 文件、224 项测试通过，Svelte 检查零错误零警告，临时目录生产构建通过。
- 新增后端覆盖：默认关闭、CLI 无密码不监听、首次密码哈希、自动端口持久化、开关/改密码、
  停用重启保留、SSE 断开、旧会话不能绕过新密码、远程禁止修改设置、bootstrap 拒绝、
  CSRF、Host 伪造、端口冲突、保存失败、启动监听失败后的重试、并发设置与登录限流；
  密码登录、Cookie 属性/轮换/有效期/容量、跨 Host 重放、跨站登录、请求大小/字段验证和不再接受 Basic。
- 信任设备后端集成测试：勾选时仅保存随机凭据摘要、重启服务免密、未勾选重启失效；
  改密码、停用再开启、换端口再换回、取消勾选及到期后不能复用旧凭据；写入失败保留旧登录，
  并发登录不能在密码修改后重新引入已撤销信任。路由契约覆盖包括监听层提供的认证端点。
- 真实 Chrome：完整 46/46 通过；新增 1280px 和 390px 两组远程访问流程，使用真实 LAN IP 登录，
  验证本机开关、自有登录页仅一个密码字段、错误密码的页内提示、成功返回原路径和查询参数、刷新保留登录、
  改密码后旧会话失效并在站内重新登录、远程只读安全设置及关闭监听；另检查远程端口 loopback 不能免登录。
  登录页默认不勾选信任，显式勾选产生 90 天 Cookie；在内存中保留 Cookie 后重建浏览器上下文，
  无需密码即可重新访问工作台，登录页书签也自动跳转。服务重启持久化由上述后端集成测试验证。
  所有密码在测试进程内生成，没有 trace/HAR 或含凭据 URL。截图仅在输入密码前或密码框清空后拍摄。
- ShellCheck、浏览器脚本语法、OpenAPI 校验和差异检查通过。OpenAPI 仍有原有的 health 4XX
  与 bootstrap 2XX 两条警告；未虚构响应来消除警告。
- 本轮未操作真实 Wails 窗口，未调用真实 Codex/OpenCode 模型，未构建或发布公开制品。
  桌面集成证据是共用服务的测试和 desktop 自动化，不替代真实 `.app` 验收。

所有验收使用新建临时数据目录，不重启正式服务，不修改正式数据库。浏览器证据保留于仓库外的
`am-trusted-device-XOfUXo/browser-final/` 临时目录；此前 Basic 或未支持设备信任的旧证据不作为当前流程证据。
