# Codex 原生精确文件写权限

固定契约：Codex `rust-v0.163.0`，commit
`83086188ae0614bd882818ff1aa23f3194962012`；真实二进制
`app-server generate-json-schema --experimental`。

旧 `workspaceWrite.writableRoots` 无法表示精确 IPC 权限：上游
`protocol/src/protocol.rs::SandboxPolicy::get_writable_roots_with_cwd` 总会附加 CWD，
默认还附加 `/tmp`、`TMPDIR`。即使只传本 Run IPC，也能修改同 CWD 中其他 Run 的请求。

现在使用原生 named permissions profile：

* filesystem `:root = read`，`:workspace_roots` 的 `.` 与 `.git` 为 write，network disabled；
  显式 `.git` 子项保留授权根内部的合法 git add/commit，不放行根外 gitdir；
* 工厂冻结已授权业务根的规范路径，不把 CWD 推断成业务根；
* 每次 `thread/start`、`thread/resume` 和 `turn/start` 都完整替换
  `runtimeWorkspaceRoots`：普通 Run 为业务根 + 本 Run IPC，只读 Run 只有本 Run IPC；
  没有根时发送空数组，不能继承旧 Run；
* 特别在已加载线程的 Resume 中，原生可能忽略 thread 参数，所以 turn 必须再次替换；
* 写授权仍只来自完整替换的 runtime roots，不把旧 Run 路径写入可复用配置；
* 每个有 FileBridge 的 Run 只在已有 IPC 根内创建私有 `.tmp`（0700），通过真实
  `shell_environment_policy.set` 设置 TMPDIR/TMP/TEMP；zsh 的 heredoc 不使用 TMPDIR，
  所以还把其有文档定义的 TMPPREFIX 设置成该 `.tmp/zsh`。不增加 write root；
* 原生已加载线程会忽略 resume 的 config override，turn/start 又没有 shell environment
  字段。因此 Resume 在串行 Run 边界重启私有 app-server，再从同一持久 CODEX_HOME 恢复
  原线程，保证新 Run 的 temp/config 替换生效。没有 bridge 就不创建/注入 temp；
* 工厂只检查原 auth 来源、真实 symlink 目标与私有配置目录的路径元数据，不读认证正文。
  若业务根覆盖私有路径，通过 native filesystem 精确 read carveout 保护包含该路径的
  首级子树，防止直接/alias 写、原子替换、删除及移动祖先目录；
  原生同路径 write 优先于 read，故遇到重叠根或 `.git` 写规则可能重新开放该子树时，
  工厂/Run 在 turn 前拒绝不安全 scope，而非接受无效护栏；
* 本机 CODEX_HOME 继续持久化，配置/Skills 与 auth 链接机制不变，没有复制凭据；
* 普通 Run 保留原生规则/Skill 审批，但 `granular.sandbox_approval=false` 与
  `request_permissions=false` 禁止沙箱外重试或额外权限请求。只读 Run 使用 `never`；
* initialize 原生 userAgent 必须是稳定版 0.163.0 或更新版本；还要求线程响应确认
  named profile 与 runtime roots，新线程根列表必须精确匹配。旧 serde 可能忽略未知字段，
  不能把“没报 RPC 错误”当支持；不兼容时在任何 turn 前失败，不回退 legacy 或全权限。

`TEAMVA_CODEX_TEST=1 go test ./provider/codex -run NativeCodexExactIPC -v`
使用真实 CLI、隔离 HOME/CODEX_HOME 和 loopback 确定性 Responses fixture，真正执行
Shell、原生 apply_patch、git init/add/commit/status 与测试脚本，验证业务根、IPC、
配置/Skills、fakeauth source/alias/祖先移动及 hardlink、heredoc/Python/Node 私有 temp、
只读、无 bridge、Resume 换 scope 和并发。
不访问真实登录文件、不调用外部模型。

边界：本策略限制原生沙箱中的执行与文件工具，不把读取公开为新的产品授权承诺；
自定义 MCP 本身是用户信任的外部程序，不因本 profile 自动获得宿主进程级沙箱。
没有开放公共临时目录；FileBridge Run 的默认临时写入自动落在本 Run IPC；其他缓存可显式
放到业务根，不能以工具需要缓存为理由放行整个宿主 /tmp。macOS 的 `/usr/bin/git` 定位 shim 可能报告
xcrun 缓存写入警告；真实 git 与默认 PATH 的 git status 均已在原生测试通过。

## 注册 linked worktree 的元数据

0.163 会对 worktree `.git` 文件解析到的 gitdir 自动加更窄的 readonly carveout。
因此即使主仓库 P 与 worktree W 都已授权，P 的 `.git` write 仍可能被该默认规则覆盖。
仅在以下校验全部通过后，才对这个已授权 gitdir 添加原生绝对路径精确 write 项：

* W 的 `.git` 为小型 regular file，读取前后 inode/大小/mtime 不变；
* gitdir 位于真实 `<已授权主根>/.git/worktrees/<id>` 布局，目录/control files 非 symlink；
* gitdir 的 `gitdir` 回指与 W `.git` 是同一 inode，`commondir` 与另一显式授权主根的
  `.git` 目录 inode 一致；
* 不与任何 private/auth readonly 子树交叠，不将 glob 当精确路径。

这不是新增父仓库授权：W 单独授权、纯子目录、任意伪造 `.git`、readonly 都不会获得此项。
真实 native fixture 覆盖 P/S/W 注册后的 git add/commit，以及上述拒绝分支。
