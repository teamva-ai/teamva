# Teamva 构建、发布与可靠性

## 1. 当前发布目标

当前只发布 macOS universal2 桌面 ZIP。首版不发布 Windows/Linux 包，也不依赖 Cloudflare、Docker、
Pages 或单独安装的 CLI。

## 2. 构建

```bash
TEAMVA_VERSION="$(tr -d '\r\n' < VERSION)" \
  ./scripts/release/build-desktop.sh
```

脚本必须：

1. 构建 Web（默认且仅支持同源 API）；
2. 暂存唯一 Web 构建到忽略的 `desktop/frontend/dist`；
3. 用 Wails v2.13.x 构建 darwin/universal；
4. 注入版本并验证 arm64 + x86_64；
5. 生成 ZIP、`VERSION` 和 `SHA256SUMS`。

当前开发制品未签名、未公证。若决定面向普通 macOS 用户公开分发，签名/公证是独立发布门禁。

## 3. 更新

应用从 HTTPS `/downloads/desktop` 读取语义化 `VERSION` 和 `SHA256SUMS`，只下载固定名称 ZIP，限制大小并
验证 SHA-256。它不会替换正在运行的 `.app`；用户退出后手工解压替换。

发布时必须确认缺失制品返回明确 404，不能由 SPA fallback 伪装成 200 HTML。

## 4. 本地生命周期

- 单实例锁防止两个进程同时打开数据库；
- `/readyz` 成功后才导航 WebView；
- 服务只监听随机 loopback；
- 关闭顺序为停止请求、scheduler、Runtime/Provider、checkpoint SQLite、HTTP；
- SIGKILL 后下一次启动恢复 uncertain command、未 ACK event 和 session；
- 退出后不得遗留 Codex/OpenCode 子进程。

## 5. 日志与诊断

- 日志 0600，5 MiB 轮转，最多三份；
- 原生菜单可打开数据目录并导出脱敏诊断 ZIP；
- 诊断包含版本、平台、服务状态、数据库文件元数据和脱敏日志；
- 诊断不含数据库内容、WAL、runtime state 或 Provider 凭据。

## 6. 数据边界

当前产品不需要在线备份、在线恢复、云同步或数据库损坏修复。
可靠性要求是：旧基线或完整性检查失败时保留原数据并拒绝启动，绝不自动升级、删除或重建数据库。

## 7. 发布验收

- 从干净 commit 构建；
- 完整 race/vet/Web/浏览器/Provider 门禁通过；
- Finder 启动、第二实例、菜单、退出和休眠恢复通过；
- ZIP 解压后应用可启动，版本与校验清单一致；
- 更新 URL、404 行为和 checksum 真实验证；
- 创建 release tag 并保存不含秘密的验收报告。
