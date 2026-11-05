# Teamva 实施路线图与工程计划

## 1. 当前结论

本地工作台的主体已经实现。当前工作是完善文档、持续验证、
真实应用验收和可下载安装制品收口。

## 2. 已完成基线

- Wails macOS 应用壳、单实例、原生菜单；
- loopback HTTP、一次性 bootstrap Session、capability 驱动 UI；
- SQLite control/runtime Store、当前基线、WAL 与崩溃恢复；
- Codex/OpenCode Embedded Runtime；
- Project、Resource、Agent/Skill、Issue/comment/mention、Run/event；
- Policy、Approval、Memory、Schedule、Audit；
- 多 Agent 并行与间接循环委派；
- 日志、诊断、更新检查和 universal2 未签名构建；
- 本地 API、浏览器、真实 Provider 和 SIGKILL 验收脚本；

## 3. 首发前 P0

### 3.1 产品与文档

- README、00–12、AGENTS.md 全部以桌面主产品为准；
- 删除旧字段 fallback、旧协议分支和旧产品 onboarding 文案；
- OpenAPI、UI 文案和 Issue/Agent 术语保持一致。

### 3.2 持续验证

- 建立 CI：Go race/vet、Web、OpenAPI、ShellCheck、SQLite 当前基线及旧库拒绝；
- 固化多 Agent 并行、`A → B → A` 循环、停止和崩溃恢复；
- 保存不含凭据的测试报告。

### 3.3 桌面全功能验收

- 从 Finder 启动真实 `.app`；
- 逐项操作 Project、Resource、Agent、Skill、Issue、Memory、Schedule、Policy、Approval、Audit、Settings；
- Codex/OpenCode 各完成真实 Run、工具评论、计划任务和 Agent 委派；
- 验证单实例、菜单、日志、诊断、退出、重开、SIGKILL 和子进程清理；
- 验证 1280×820、最小窗口和移动宽度 Web 回归。

### 3.4 发布制品

- 决定公开首发是否启用 Developer ID 签名和公证；
- 构建 universal2 ZIP、VERSION、SHA256SUMS；
- 部署 `/downloads/desktop/*`，缺失文件必须 404；
- 真实下载、校验、解压替换和版本升级；
- 固化 release commit/tag。

## 4. 发布后 P1

- 基于真实指标增加 Agent 并发/资源提示，但不禁止间接循环委派；
- 更清楚的循环委派可视化、费用提示和急停反馈；
- 原生目录选择器和系统通知；
- Windows 桌面评估；
- 更多 Provider Adapter；
- 大型项目的搜索、筛选和归档体验。

在线备份、在线恢复、云同步和数据库损坏修复不在当前路线图内。

## 5. 工程节奏

1. 更新契约和安全不变量；
2. 实现与自动化测试；
3. 在真实 API、浏览器、Wails 和 Provider 边界验收；
4. 更新文档 12 当前证据；
5. secret scan、`git diff --check`；
6. 独立 commit；
7. 完整 release gate 通过后才 tag/发布。

## 6. 发布前验收标准

- 主线文档无云端/CLI 默认路径冲突；
- 完整自动化门禁通过；
- 真实 Wails、Codex、OpenCode 和多 Agent 循环委派通过；
- 重启、SIGKILL、停止、审批、计划任务和诊断通过；
- 公开桌面制品可下载且 SHA-256 一致；
- 无 P0/P1 安全缺陷；
- release commit/tag 和无秘密验收报告完成。
