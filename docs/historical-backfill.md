# 一次性历史回填运行手册

历史命令复用 daemon 的 Journey 状态机，但每天只生成一次确定性批次。普通 daemon
不携带历史上下文，行为和原有账本身份保持不变。

## 前置条件

- IAM `mockConsumer.enabled=true`；历史回填默认将 IAM 并发限制为 1，共享密钥只通过
  `IAM_MOCK_CONSUMER_SHARED_SECRET` 注入。
- qs-server apiserver 与 collection-server 使用相同的
  `QS_HISTORICAL_CONTEXT_SECRET`，并已启用限定 Org、`2025-01-01..2026-07-27` 的历史开关。
- apiserver 设置 `historical_seed.pause_plan_scheduler: true`；回填期间停用独立 Plan scheduler。
- Daily Simulation 配置 `countMin: 40`、`countMax: 200`，Journey 权重为
  `10/15/25/50`，Plan 列表、入口和已发布 target 版本均已确认。
- 已保存 Statistics 回填前基线；worker 正常运行，且没有同时运行普通 seeddata daemon。

## 构建与预检

```bash
GOTOOLCHAIN=go1.25.9 go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.9 go build -trimpath \
  -o tmp/bin/seeddata \
  ./cmd/seeddata
export IAM_MOCK_CONSUMER_SHARED_SECRET='<secret>'
export QS_HISTORICAL_CONTEXT_SECRET='<secret>'
```

历史模式默认使用父场景 12、submission 6、downstream reconciler 4、内存调度队列 24、
持久 pending 安全高水位 4096、stage reader 6、IAM 1 路并发。IAM 限制同时覆盖
首次创建和历史 guardian 会话恢复，避免恢复批次绕过限流形成登录洪峰。可以在
`historicalBackfill` 配置块设置，也可以用 `--parent-workers`、`--submission-workers`、
`--report-workers`、`--report-queue-capacity`、`--pending-high-watermark`、
`--stage-read-workers`、`--iam-workers` 临时覆盖。
普通 daemon 不读取这些参数。

submission worker 只负责把答卷提交到 accepted，并在同一个 Bbolt 状态库中持久化
`AnswerSheetID` 和 `submitted` downstream 记录，随后立即成功返回。后台 reconciler 从 Bbolt
扫描 `submitted/downstream_pending`，以有限 worker 异步解析 Assessment、Outcome 和 Report，
全部历史 stage（包含 `report_generated`）齐全后改为 `verified`。内存队列仅是调度窗口：达到
`reportQueueCapacity` 时暂停新的 downstream 调度，不阻塞 submission worker，也不会丢失 Bbolt
中的 pending。只有 durable pending 数达到 `pendingHighWatermark` 时，才停止新的远端提交并让
批次以明确的高水位错误退出；修复下游吞吐后使用同一批次 `--resume`。

先用 3 天范围执行本地或 staging 验证；成功后使用正式批次 ID：

```bash
tmp/bin/seeddata historical-backfill \
  --config configs/seeddata.yaml \
  --state-dir /secure/path/seeddata-historical-state \
  --from 2025-01-01 \
  --to 2026-07-27 \
  --batch-id hist-20250101-20260727-v2
```

checkpoint 分为两个游标：当天全部提交任务已完成后推进 `submitted_through`，最终全批次
downstream drain 和严格 stage 验收通过后推进 `verified_through`。因此提交可以持续向前，
报告处理不会占用父场景或 submission worker；命令退出前仍会统一等待并验收所有 pending。
单场景 `429/5xx`、网络中断或请求超时会被记录为提交缺口，但不会中断后续日期；
`submitted_through` 停在第一个缺口，全部日期处理完后才返回汇总错误。配置/身份/payload conflict、
Bbolt 持久化失败、高水位熔断和全局取消仍立即停止；单日瞬时失败超过父场景数的 5% 也会触发
系统级熔断，避免下游整体故障时继续放大流量。所有失败都会保留两个游标和 Bbolt pending，
修复原因后使用同一批次、相同配置和相同版本恢复：

```bash
tmp/bin/seeddata historical-backfill \
  --config configs/seeddata.yaml \
  --state-dir /secure/path/seeddata-historical-state \
  --from 2025-01-01 \
  --to 2026-07-27 \
  --batch-id hist-20250101-20260727-v2 \
  --resume
```

Manifest v2 会冻结每个父场景的 guardian/child Profile，恢复时不再重新生成姓名、手机号、
登录标识、生日或性别。IAM 登录标识只依赖 batch、业务日期和 index，不依赖显示姓名。
Manifest v1 缺少这项不变量，因此会在任何 IAM 登录或业务 HTTP 请求前被拒绝；不得把 v1
状态直接用于 v2 恢复。完成旧批次远端数据清理后，应使用新的 batch ID 创建全新 v2 状态。

命令会创建权限为 `0600` 的 `historical-state-v2.db`。CLI 首次创建可以不传 `--resume`；正式
GitHub Actions 始终传 `resume=true`，空批次仍会安全创建初始状态，且不会导入 v1 ledger。后续
恢复必须复用同一个 v2 batch。同一批次已有 writer 或状态身份冲突时命令直接退出；不要删除
有效 v2 数据库或更换 batch ID 绕过冲突。

历史 `scenario_id` 同时是本地、服务端 stage 和提交请求使用的持久幂等身份。恢复同一 v2 批次时，
如果 manifest 的冗余 `journey` 字段与合法 `scenario_id` 中的 journey 段不一致，runner 会保留
原 `scenario_id`、按其中的 journey 恢复，并记录校正数量和样例 ID；日期、index、target、
entry、Plan 或非法 journey identity 等其他冻结身份冲突仍会立即停止。
`scenario_id` 中的用户 index 是 `Profile.Index`，范围为 `1..当日人数`；runner 仅在内存中将其
映射为 `0..当日人数-1` 的 worker job index，不会改写持久身份。

运行中每 15 秒输出当前自然日、父场景进度、已发现/完成 submission、吞吐、
submission in-flight、失败数和 ETA。自然日仍然串行执行；没有提交缺口时连续推进
`submitted_through`，出现缺口后仍可处理和持久化后续日期，但游标不会越过缺口。
`verified_through` 只代表已经完成严格 stage 验收的连续范围，不能用提交游标替代最终完成判定。

## ServerA 内网一次性容器

先构建静态二进制镜像：

```bash
SEEDDATA_HISTORICAL_IMAGE=seeddata-runner:historical \
  ./scripts/build_historical_container.sh
```

准备权限为 `0600` 的环境文件，至少包含 IAM 登录凭据、
`IAM_MOCK_CONSUMER_SHARED_SECRET` 和 `QS_HISTORICAL_CONTEXT_SECRET`。不要把密钥写入 YAML：

```bash
install -m 0600 /dev/null /secure/path/seeddata-historical.env
```

设置宿主机路径并运行：

```bash
export SEEDDATA_HISTORICAL_CONFIG=/opt/seeddata-runner/configs/seeddata.yaml
export SEEDDATA_HISTORICAL_STATE_DIR=/secure/path/seeddata-historical-state
export SEEDDATA_HISTORICAL_ENV_FILE=/secure/path/seeddata-historical.env
export SEEDDATA_HISTORICAL_BATCH_ID=hist-20250101-20260727-v2
export SEEDDATA_HISTORICAL_RESUME=1

./scripts/run_historical_container.sh
```

镜像内进程使用 UID/GID `10001:10001`；配置文件必须允许该用户读取，状态目录必须允许该
用户写入。脚本会用同一镜像用户实际创建并删除一个预检文件，不能写时会在回填前退出。
第一次 `--resume` 若尚无 v2 数据库，脚本会安全创建全新状态；正式 v2 批次不导入
v1 manifest 或旧 submission ledger。

脚本会先确认 `infra-network`、状态目录写权限、三个容器 DNS 名称和健康接口；任一失败即
停止，不回退公网。正式容器固定使用：

- QS：`http://qs-apiserver:8080`
- Collection：`http://qs-collection-server:8080`
- IAM：`http://iam-apiserver:9080`
- IAM login：`http://iam-apiserver:9080/api/v2/authn/login`

容器以非 root 用户、只读根文件系统、`cap-drop=ALL`、`no-new-privileges` 运行；配置只读
挂载，状态目录读写挂载，环境文件由 Docker 读取后注入。JWT、IAM shared secret 和历史
HMAC 校验全部保留。

## GitHub Actions 部署

完整的首次配置、ServerA 前置检查、手动启动、审批、监控、停止、同 revision 恢复、常见失败和
Statistics/K6 收尾步骤见
[GitHub Actions 历史回填部署与操作手册](./github-actions-historical-backfill.md)。本节只保留契约摘要。

仓库提供三个 workflow：

- `CI`：main/PR 自动执行全库测试、历史关键包 race、部署契约、Linux 静态构建和历史镜像构建。
- `Historical Backfill Deploy`：只允许从 main 手动触发，经 `production` Environment 审批后，
  发布 commit SHA 不可变镜像并在 ServerA 启动 systemd 托管的一次性回填。
- `Historical Backfill Control`：手动执行只读 `status` 或带确认词的 `stop`；不会删除状态或业务数据。

部署 workflow 不传输 IAM/QS 业务 Secret。ServerA 必须预先存在权限为 `0600` 的
`/secure/path/seeddata-historical.env`，至少包含：

```text
IAM_USERNAME
IAM_PASSWORD
IAM_MOCK_CONSUMER_SHARED_SECRET
QS_HISTORICAL_CONTEXT_SECRET
```

Repository/Organization 需要提供与现有生产部署一致的 ServerA 连接配置：

```text
vars:    SVRA_HOST, SVRA_PUBLIC_HOST(optional), SVRA_USERNAME,
         SVRA_SSH_PORT(optional), SVRA_SSH_FINGERPRINT
secrets: SVRA_SSH_KEY or SVR_MINI_SSH_KEY, SVRA_SUDO_PASSWORD(optional)
```

可选 production Environment Variables：

```text
SEEDDATA_HISTORICAL_STATE_DIR
SEEDDATA_HISTORICAL_ENV_FILE
SEEDDATA_HISTORICAL_BASELINE_FILE
SEEDDATA_HISTORICAL_DEPLOY_ROOT
SEEDDATA_HISTORICAL_LOG_DIR
```

缺省路径与本文 ServerA 示例一致。正式部署必须输入确认词
`START_HISTORICAL_BACKFILL`；正式批次固定使用原日期范围和 `resume=true`。部署完成后进程由
`seeddata-historical-backfill.service` 托管，Action 退出不会终止回填。停止操作必须输入
`STOP_HISTORICAL_BACKFILL`，只停止 unit/容器并保留 bbolt、manifest、日志和服务端事实。

首次使用前在仓库 Settings 中创建 `production` Environment 并配置审批；将 CI 的
`Run Tests`、`Historical Backfill Race Tests`、`Deployment Contracts` 和 `Linux Build`
设置为 main 分支 required checks。

不得更换 batch ID 或幂等键来绕过 payload conflict。出现 target/Plan 版本漂移时，恢复原冻结版本或新建经审批的批次，不能继续原批次。

## 修复旧 runner 已创建的 Testee 报到时间

如果批次曾由不携带 `testee_created_at` 的旧 runner 启动，先保持 runner 停止，并用原
manifest 生成精确 ID 范围的修复 SQL。该命令只读取本地状态并输出 SQL，不连接数据库：

```bash
umask 077
tmp/bin/seeddata historical-testee-time-repair-sql \
  --state-dir /secure/path/seeddata-historical-state \
  --batch-id hist-20250101-20260727-v2 \
  --expected-database "$QS_DB_NAME" \
  > /secure/path/hist-20250101-20260727-v2.testee-time-repair.sql
```

检查 SQL 中的数据库、Org、Testee 数量和时间范围，然后显式确认并执行：

```bash
mysql --defaults-extra-file="$QS_MYSQL_CNF" \
  --init-command="SET @qs_testee_time_repair_confirm='REPAIR_HISTORICAL_TESTEE_CREATED_AT'" \
  "$QS_DB_NAME" \
  < /secure/path/hist-20250101-20260727-v2.testee-time-repair.sql
```

SQL 只更新 manifest 明确归属本批次的 Testee ID，数据库、Org 或行数不匹配时会在事务前拒绝。
修复命令可幂等重跑，并显式保留原 `updated_at`。确认报到日期分布正确后，才使用同一批次执行
`historical-backfill --resume`。

## 只读检查

```bash
tmp/bin/seeddata historical-verify \
  --config configs/seeddata.yaml \
  --state-dir /secure/path/seeddata-historical-state \
  --batch-id hist-20250101-20260727-v2

tmp/bin/seeddata historical-manifest \
  --state-dir /secure/path/seeddata-historical-state \
  --batch-id hist-20250101-20260727-v2
```

`historical-verify` 同时检查本地 checkpoint/manifest 和 qs-server stage ledger；需要
Assessment 的场景必须存在 Assessment、Outcome、Report，Plan 子场景必须存在 task open/complete。

## 后续和失败处理

runner 完成后，按 qs-server 运行手册执行 19 个 Statistics repair/validate 窗口、最终 publish
和精确 Statistics 对账。失败时保留 `.seeddata-cache/historical`、服务端 stage ledger 和日志。
需要回滚时，先停 runner、worker、Plan scheduler，使用 manifest 和 qs-server 批次回滚工具；不得按 Org
或模糊时间范围清理。
