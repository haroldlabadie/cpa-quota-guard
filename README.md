# CPA 额度保护

独立的 CPA 原生 Go C ABI 插件，为显式白名单账号保留额度。当前 `0.2.0`，Linux amd64，ABI 1、RPC schema 6；宿主最低接口基线为 CPA v8.0.33 / `2422c6b`。它不生成模型请求，不管理预热计划，可单独安装，也可与 [cpa-warmup](https://github.com/haroldlabadie/cpa-warmup) 同时运行。

## 行为

- Codex：五小时剩余 ≤8% 或每周剩余 ≤3% 时禁用整凭证。所有新鲜相关窗口都高于阈值后，才恢复可证明属于本插件的限制。
- Antigravity：Gemini 与 Claude/GPT 两族分别保护，合并 `excluded_models`；保留原有人工排除，不因一族额度不足停用另一族。
- 未知、缺桶、过期或探测失败的额度按需要保护处理，绝不假设剩余 100%。单次管理接口瞬断时，同一身份且 90 秒内的既有样本继续参与判断并保留错误；超过新鲜度或身份变化即失败关闭。Free 等缺少五小时窗口的 Codex 无法通过此保护策略的安全恢复条件。Codex 整凭证判定只使用 `scope=all` 的主五小时/每周窗口；附加池仅展示，解析失败或耗尽不会冒充主池，主池缺失仍按未知处理。
- 凭证修改采用持久 intent：先原子写入含完整旧/目标保护字段、身份、先前所有权和 mutation ID 的事务，再 PATCH 必要字段并回读完整保护字段。PATCH 超时或回读失败保留事务，重启后先 reconcile；只接受“严格旧快照”“完整目标快照且 mutation 证据吻合”或显式冲突。无法证明的变化进入 sticky conflict，不会被后续字段恢复自动认领。宿主无 CAS，最后回读至 PATCH 之间的人工并发写入仍是已知边界。
- 只操作配置白名单。停用插件、关闭或删除规则均保留已有停用和模型排除，避免停守护成为绕过保护的方式。
- 人工停用不自动启用。丢失本地所有权、同名换账号、保护字段变化或无 Token / 其他字段变化的同内容重写，会释放自动恢复资格；Token-only 刷新和无关的 prefix/note 等变化保留所有权。

默认每 20 秒查询受保护账号。额度查询和字段 PATCH 经过 CPAMP 的管理代理及其 mutation coordinator；CPA 原生 RPC 只读库存、凭证与 runtime。插件不提取 Token 自行请求供应商，不调用整文件 `host.auth.save`。

## 安装

下载对应版本的 `cpa-quota-guard_0.2.0_linux_amd64.zip`，按同 Release 的 `checksums.txt` 校验；包内另含 `BINARY_SHA256SUMS`。zip 根目录只有一个 `.so`，兼容 CPA Plugin Store。`.so` 和数据目录放入 CPA 插件挂载目录，勿覆盖其他插件。

```sh
install -d -m 0700 /CLIProxyAPI/plugins/cpa-quota-guard-data
install -m 0644 cpa-quota-guard.so /CLIProxyAPI/plugins/cpa-quota-guard-v0.2.0.so
install -m 0600 settings.example.json /CLIProxyAPI/plugins/cpa-quota-guard-data/settings.json
```

修改 `settings.json` 中的虚构账号邮箱，只列入需要保护的账号。用安全的文件操作把 **CPAMP panel admin credential** 写入 `management-key.txt`，权限必须 0600；不要把 Key 写入命令历史、YAML、Git 或页面。该凭据不保证与 CPA 原生管理 Secret 相同。

宿主配置示例（合并到现有 `plugins.configs` 映射，不整文件覆盖）：

```yaml
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins
  configs:
    cpa-quota-guard:
      version: 0.2.0
      enabled: true
      priority: 0
      mode: observe
      data_dir: /CLIProxyAPI/plugins/cpa-quota-guard-data
      secret_file: /CLIProxyAPI/plugins/cpa-quota-guard-data/management-key.txt
      cpamp_url: http://cpamp:18317
      check_seconds: 20
      probe_seconds: 20
```

容器内的 CPAMP 地址必须由实际 Compose 网络核实。HTTP 只允许固定私网/回环基地址及代码内固定管理路径；不读取环境代理、不跟随重定向。供应商查询 URL 固定，Antigravity 优先正式入口，备用入口只补齐缺失的完整模型族，绝不覆盖先观察到的低额度。

首次以 `mode: observe` 验证库存、身份、额度与页面。observe 不 PATCH 凭证、不发布面板快照、不写运行 state；应使用独立临时数据目录做上线前观测。迁移正式状态、确认没有旧守护同时运行，再切换到 `mode: apply`。apply 要求显式存在 `settings.json` 和 `state.json`；缺失状态时停止，不把曾有的所有权默认为空。首次安装到全新独立目录时，可用 `guard-state initialize-state` 输出带 `fresh-install` 标记的初始状态，经新文件安全写入后再启动。已有安装丢失状态必须从备份/迁移恢复，不能运行初始化命令清空历史。保留比例是剩余百分比，`8` 表示剩余 8% 时保护。

```json
{"schema":1,"rules":[{"email":"person@example.invalid","provider":"codex","enabled":true,"reserve_five_hour":8,"reserve_weekly":3}]}
```

不要让两份守护同时修改相同账号。停止插件会取消 HTTP、等待 worker 退出，再释放 `worker.lock`；宿主热更新须调用 `plugin.quiesce`，最终 shutdown 同样幂等停止。

## 管理页面与接口

CPA 插件菜单的“额度保护”提供账号邮箱、供应商、保护开关、五小时/每周保留百分比、账号剩余及原因。复用同源 CPAMP 管理会话，或手动输入只保留在页面内存的 CPA Management Key。HTML 是不含账号/Key/状态的公开静态 shell，CSP nonce 限制脚本；敏感数据只经宿主鉴权管理路由读取。

| 接口 | 用途 |
| --- | --- |
| `GET /v0/management/plugins/cpa-quota-guard/status` | 版本、模式、worker、最近 tick/错误、revision |
| `GET /v0/management/plugins/cpa-quota-guard/settings` | 规则及 ETag |
| `PUT /v0/management/plugins/cpa-quota-guard/settings` | 必须带精确 `If-Match`，无匹配返回 412，缺失返回 428 |
| `GET /v0/management/plugins/cpa-quota-guard/observations` | 脱敏账号身份摘要、额度、原因、最近动作 |

`observations` 的 schema 为 1，每个观测含 `name/provider/auth_index/identity/updated_ms/sample`，以及按状态出现的 `manual/action/transaction_id/transaction_phase/orphan_reason`；`sample` 为 `observed_ms/windows`。窗口保留 `id/kind/scope/remaining/end_ms/duration_ms`。消费方必须自行校验身份、完整性和 ≤90 秒新鲜度，不能把无观测当有可用额度。该接口是可选复用优化，warmup 也能独立采样。

apply 模式将额度以只写展示方式发布到 CPAMP `quota-snapshots`。发布失败只显示 `snapshot_error`，不改变保护结论；零用量五小时窗口不凭移动截止时间伪造周期起点。该展示快照不作为本插件判断输入。

## 状态、迁移与回滚

`data_dir/state.json` 为 schema 2，目录 0700、文件 0600，临时文件写入、fsync、rename、目录 fsync；内容损坏则停止，绝不清空重建。Linux `flock` 的 `data_dir/worker.lock` 保证同一数据目录只有一个实例。旧 schema 1 可读；只有 apply 模式取得 worker 锁后才原子升级，observe 不写状态。`settings.json` 独立持久化，页面更新与凭证 mutation 串行协调。`journal` 有界保留已完成转换，未决事务与冲突位于独立字段，不因日志淘汰。新状态不能被旧二进制无损读取，回滚前须先处理事务/冲突并导出兼容状态；有不可表示状态时保持插件停止。

```json
{"schema":2,"plugin":"cpa-quota-guard","legacy":{"version":2,"accounts":{}},"ownership_proofs":{},"transactions":{},"conflicts":{},"retired":[],"journal":[],"observations":{}}
```

`legacy.accounts` 无损保存旧记录，仅作兼容归档；自动恢复**只信** `ownership_proofs`。键为 auth 文件名，值为 `{disable?:Snapshot,family?:Snapshot}`。Snapshot 包含身份摘要、受保护字段安全快照及其 SHA256、Token 字段摘要、无关配置字段摘要、mtime_ns。原始 Token 不落盘。

`guard-state` 供离线迁移器使用：

- `guard-state fingerprint`：stdin 为 `{auth:<原始凭证对象>,mtime_ns,name,auth_index,provider,email}`，stdout 为脱敏 Snapshot。
- `guard-state verify`：同上，另加 `proof:Snapshot`，输出 `{valid,snapshot}`。只有 valid=true 才能转移自动恢复资格。
- `guard-state validate-state`：stdin 为完整候选状态，使用插件相同解析器，成功输出 `{valid:true}`，失败仅输出通用错误。
- `guard-state state-digest`：stdin 为完整 state，输出覆盖 legacy、migration、所有权、事务、冲突、退役记录、journal 和 observations 的规范化 SHA256；离线 repair 必须以该值绑定审阅对象。
- `guard-state initialize-state`：不读取 stdin，不写文件，输出带创建时间与 `fresh-install` 标记的空状态，仅供全新独立目录首次安装。
- `guard-state diagnose-orphans --data-dir DIR --current FILE`：只读列出旧 revision 与 native proof 的孤儿原因；输入文件必须是 0600 的脱敏当前快照，不能由同名账号推断所有权。
- `guard-state acknowledge-conflict --data-dir DIR [--apply]`：stdin 为精确状态摘要、账号、当前身份和原因；默认 dry-run，apply 获取 `worker.lock`、重验摘要并原子保存。承认人工接管时必须同时存在同 ID 的未决事务，才把完整事务移入 `retired` 审计并清除对应自动所有权；仅有孤立 conflict 映射会拒绝，不能靠它删除所有权。
- `guard-state verify-native-proof --data-dir DIR --current FILE [--apply]`：只验证已存在的 native proof 是否仍与当前完整保护字段、身份和 owner 吻合；不创建证明，没有 proof、存在 transaction 或 conflict 时拒绝。旧 revision 的重新认领必须由 Python 迁移工具用 `legacy_owned` 重算和稳定现场读取完成，原生 helper 不接收调用方自报的当前快照。

原始凭证只能从安全管道输入，不能作为命令行参数；helper 从不回显输入。旧 Python revision 必须先用旧 Python 原算法、实时文件和 mtime 验证，再由该 helper 生成新摘要；不要比较 Python/Go 的 JSON hash。新状态可带 `migration` 对象保留来源 SHA、版本、迁移 ID 与证明。离线孤儿修复必须显式指定账号、完整 state 摘要（由 `guard-state state-digest` 对原始文件计算）和操作原因；apply 另需 `worker.lock`、现场/状态重验、create-only 备份、单次原子替换与目录 fsync。

回滚先 quiesce 并获取 worker.lock，导出**最新**所有权，再结合 live 凭证验证并重新计算旧 Python revision。不能恢复迁移前的陈旧状态；证明失败时保留现有限制作为人工 baseline。额度守护和 warmup 的状态互不写入。

## 安全边界

CPA v8.0.33 的字段接口没有 CAS。插件写前读取、校验身份/保护快照，写后读取 disk 与 runtime；失败不登记成功。最后读取与 PATCH 之间的并发窗口仍存在；Token 刷新同时人工重复同内容写入无法完全区分。直接编辑凭证文件前必须先停守护。这一边界与旧路径一致，不能将插件化解释为原子事务。

受保护字段为 `disabled`、owner/reason、family_control 和 excluded_models。未知外部保护字段修改不自动恢复。Antigravity 所有权不明确时把当前排除作为人工 baseline，只能添加新保护，不删除原排除。凭证 JSON、上游错误 body、Key、生成内容不进入日志/页面。`quota_guard_mutation_id` 只记录事务 UUID，用于证明一次写入对应哪个持久 intent，不包含 Token 或账号秘密。

## 构建与验证

```sh
make test
make build
```

Go 1.24、CGO、gcc、Node.js；CI 固定 Ubuntu 22.04，避免产生要求高于 Debian 12 的 glibc 产物。测试覆盖阈值与恢复、未知额度、双族隔离、人工 baseline、Token 刷新、同内容重写、身份替换、写失败、持久 intent/PATCH 超时/回读失败/重启 reconcile/sticky conflict、附加池边界、状态升级、生命周期和管理版本冲突；Node 行为测试覆盖保存、失败保留草稿与 XSS 文本渲染。CI 构建 zip 与 SHA256 并上传 artifact，正式 Release 由维护者发布。

原生 ABI/管理静态 shell 参考 MIT 项目 [key-model-access](https://github.com/haroldlabadie/key-model-access) 的模式，保留原许可证署名，并按 CPA 精确源码接口独立实现；本仓库业务逻辑源于已有 Python guard。两个插件不共享运行时可变文件；接口 schema 的变更必须同时更新契约测试，不能借复制代码绕开身份/新鲜度验证。

许可证 MIT，作者 Harold Labadie。
