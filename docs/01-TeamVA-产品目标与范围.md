# Teamva 产品目标与范围

## 1. 产品目标

Teamva 把散落在终端里的多个 Coding Agent 组织成一个本地、持久、可观察的桌面工作台。
用户围绕 Project 和 Issue 工作，可同时配置多个 Agent、查看每次执行、处理审批、保留项目记忆并运行计划任务。

## 2. 目标用户

- 已经在本机使用 Codex 或 OpenCode 的开发者；
- 希望多个 Agent 围绕同一个真实代码目录协作的个人用户；
- 需要本地数据、明确权限、可停止执行和完整审计的人。

## 3. 产品原则

1. 本地优先：无 Teamva 云服务、PostgreSQL 或 CLI 也能运行；
2. HTTP 单一边界：桌面 UI 不绕过 API 直接操作领域状态；
3. 持久化优先：Run、mention、审批和事件先落盘再执行；
4. 显式授权：文件写入、shell 与高风险工具遵守 Provider 原生权限与 Approval；Policy 规则当前仅存储，不承诺执行拦截；
5. Provider 凭据不上传：只复用本机现有登录态；
6. 多 Agent 协作可观察：谁发起、谁执行、谁再委派都写入公共评论和运行记录；
7. 允许间接循环委派：`A → B → A` 是明确能力，依赖人工急停而不是后台静默终止。

## 4. 当前能力

- 首次启动自动建立本地 User、Computer、Preferences 和内置 Skills；
- 自动探测 Codex/OpenCode 版本、登录状态、模型与能力；
- Project、代码目录/URL Resource、Agent、版本化提示词与 Skill Package；
- Issue、评论、结构化 mention、Agent Instance、Generation、Run 和 Run Event；
- 多 Agent 并行、间接循环委派、停止实例、暂停/恢复 Issue 和清空待执行队列；
- Memory、Policy、Approval、Schedule 与 Audit；
- 原生菜单、单实例、轮转日志、脱敏诊断和更新校验；
- 异常退出后的数据库、未确认命令、事件和 native session 恢复。

## 5. 首次使用

1. 用户启动 `Teamva.app`；
2. 应用创建私有数据目录和 SQLite，启动随机 loopback HTTP 服务；
3. Wails 使用 30 秒过期的一次性 nonce 换取 HttpOnly Session；
4. UI 自动显示本机已认证的 Codex/OpenCode；
5. 用户创建 Project，添加代码目录 Resource，配置一个或多个 Agent；
6. 用户创建 Issue，在评论里 `@Agent` 启动工作；
7. Agent 可发评论、调用受控工具、审批后写入，并继续 @ 其他 Agent。

## 6. 明确不做

- 桌面与云端同步或多人共享同一数据库；
- 局域网访问桌面 HTTP 服务；
- 上传或代管 Codex/OpenCode 凭据；
- 在线备份、在线恢复或数据库损坏自动修复；
- 自动迁移历史远端 PostgreSQL 用户数据；
- 首版 Windows 桌面包；
- 将 Wails binding 建成第二套业务 API；
- 因检测到 Agent 间接循环而自动停止委派。

## 7. 验收标准

- 完全断网且没有 PostgreSQL/Teamva CLI 时可以启动；
- 使用真实 Codex 和 OpenCode 完成 Issue 评论、工具、审批、委派与计划任务闭环；
- 重启或 SIGKILL 后持久状态一致，活动 Run 不永久卡在 running；
- 桌面和移动宽度的真实 WebView/Chrome 页面可用，本地 UI 不出现远端登录和设备注册；
- 安装包来源、版本和 SHA-256 可验证；
- 所有自动化门禁和本机真实边界验收通过。
