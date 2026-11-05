# Teamva 测试验收与质量策略

## 1. 完成定义

编译成功、接口 200 或 mock 通过都不等于桌面功能完成。每项能力必须在对应真实边界获得证据。

| 层次 | 证据 |
| --- | --- |
| Domain/Store | SQLite 单元测试、事务/并发/崩溃测试 |
| API | 真实 localhost Session/CSRF 的黑盒 CRUD 与错误路径 |
| Runtime | 真实 Codex/OpenCode、Agent Skills、工具、原生确认、停止和 session 恢复 |
| Browser | 隔离 Chrome profile、桌面/移动宽度、真实 HTTP 状态 |
| Desktop | Wails `.app` 从 Finder 启动、菜单、单实例、退出、诊断、更新 |
| Release | universal2、版本、ZIP、SHA-256、公开下载路径 |

## 2. 自动化门禁

```bash
(cd server && go test -race -p=1 ./... && go vet ./...)
(cd node && go test -race -p=1 ./... && go vet ./...)
(cd protocol && go test ./...)
(cd desktop && go test -race ./... && go vet ./...)
pnpm --filter @teamva/web check
pnpm --filter @teamva/web test
pnpm --filter @teamva/web build
shellcheck scripts/e2e/*.sh scripts/phase0/*.sh scripts/release/*.sh
npx --yes @redocly/cli@2.2.2 lint protocol/openapi/teamva.yaml
git diff --check
```

## 3. 多 Agent 必测

- 单 Agent mention；
- 三 Agent 并行 mention；
- `A → B → A` 间接循环，三个 Run 均成功且评论有序；
- self-mention 被拒绝；
- 同实例忙碌时 mention 退避，不阻塞其他 Agent；
- 停止实例、暂停/恢复 Issue 和清空队列；
- Agent 工具评论和自己的评论修订；
- 重复工具请求和重复 event 不产生重复事实。

## 4. Adapter 协议必测

- Codex 只接受当前 `contextCompaction` started/completed 事件；
- OpenCode 完成事件为 `session.execution.succeeded/failed/interrupted`，压缩通过 `session.compaction.started/ended/failed` 跟踪；压缩状态不得混入普通 response；
- 连续文本增量合并为同一消息，不按 chunk 拆行；
- 编程工具没有请求确认时不创建 Approval；
- 编程工具请求确认时，允许/拒绝决定能回传并继续当前 Run；
- 内部修正指令带 `internal` 标记，不进入公共任务评论。

## 5. 本地 API 与浏览器

- `local-api-smoke.sh`：Project、Resource、Issue/comment/revision、Memory、Policy、Schedule、Audit；
- `local-browser-smoke.sh`：一次性 nonce、HttpOnly Session、本地 UI、桌面与移动宽度；
- 浏览器测试必须使用同源生产构建，不得用 mock API 或错误的远端 Web bundle；
- 至少补一次真实 Wails WebView 人工/自动验收，Chrome smoke 不能完全替代原生壳。

## 6. 真实编程工具

`local-runtime-smoke.sh` 分别指定 `TEAMVA_LOCAL_PROVIDER=codex` 和 `opencode`，验证：

- 自动探测和认证状态；
- 本地代码 Resource 注册/验证；
- Issue mention → Run → 工具评论；
- 原生确认（只在编程工具确实询问时出现）；
- 手动 Schedule；
- native session；
- 关闭后无残留子进程。

`local-goal-runtime-smoke.sh` 使用一次性隔离 Resource 和由测试控制器在首轮审核期间才写入的 unlock，分别验证：

- 第一轮工作不能提前完成第二阶段；
- 至少一次 `goal_continuation` 和两次独立只读 `goal_audit`；
- `goal_finalize` 真实调用 `issue_comment_create` 发布最终总结；
- Codex/OpenCode 的真实 Token 用量已导入，审核用量不计入 Goal 预算；
- 最终文件内容、JSON、验收脚本和 Run 链全部通过；服务关闭后另行确认 Provider 子进程已清理。

真实 smoke 会产生实际模型调用，只在已授权的测试账号和临时数据目录执行。

## 7. 发布阻断

- race、vet、Web check/test/build 任一失败；
- 真实 Codex/OpenCode 任一未验证；
- Wails 实际应用未启动；
- local UI 暴露登录/CLI/设备注册；
- 基线或完整性检查失败会删除、改写或重建数据；
- 桌面下载路径返回 SPA HTML 或校验不一致；
- 工作区不干净、无 release tag 或验收报告包含秘密。
