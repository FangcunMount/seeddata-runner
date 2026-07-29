# 一次性历史回填运行手册

历史命令复用 daemon 的 Journey 状态机，但每天只生成一次确定性批次。普通 daemon
不携带历史上下文，行为和原有账本身份保持不变。

## 前置条件

- IAM `mockConsumer.enabled=true`；历史回填默认将 IAM 并发限制为 2，共享密钥只通过
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

历史模式默认使用父场景 16、submission 24、stage reader 16、IAM 2 路并发。可以在
`historicalBackfill` 配置块设置，也可以用 `--parent-workers`、`--submission-workers`、
`--stage-read-workers`、`--iam-workers` 临时覆盖。普通 daemon 不读取这些参数。

先用 3 天范围执行本地或 staging 验证；成功后使用正式批次 ID：

```bash
tmp/bin/seeddata historical-backfill \
  --config configs/seeddata.yaml \
  --state-dir /secure/path/seeddata-historical-state \
  --from 2025-01-01 \
  --to 2026-07-27 \
  --batch-id hist-20250101-20260727-v1
```

命令仅在当天所有场景达到终态后写 checkpoint。任何终态失败会停止在当天，修复原因后使用
同一批次、相同配置和相同版本恢复：

```bash
tmp/bin/seeddata historical-backfill \
  --config configs/seeddata.yaml \
  --state-dir /secure/path/seeddata-historical-state \
  --from 2025-01-01 \
  --to 2026-07-27 \
  --batch-id hist-20250101-20260727-v1 \
  --resume
```

`--resume` 首次发现旧 JSON 状态时，会在任何 IAM 登录或业务 HTTP 请求前，将 checkpoint、
manifest、分片 stage ledger 和可归属本批次的 submission 迁移到权限为 `0600` 的
`historical-state-v2.db`。迁移通过临时数据库校验后原子替换，旧 JSON 文件只读保留。
迁移冲突或同一批次已有 writer 时命令直接退出；不要删除数据库或更换 batch ID。

历史 `scenario_id` 同时是本地、服务端 stage 和提交请求使用的持久幂等身份。恢复旧批次时，
如果 manifest 的冗余 `journey` 字段与合法 `scenario_id` 中的 journey 段不一致，runner 会保留
原 `scenario_id`、按其中的 journey 恢复，并记录校正数量和样例 ID；日期、index、target、
entry、Plan 或非法 journey identity 等其他冻结身份冲突仍会立即停止。
`scenario_id` 中的用户 index 是 `Profile.Index`，范围为 `1..当日人数`；runner 仅在内存中将其
映射为 `0..当日人数-1` 的 worker job index，不会改写持久身份。

运行中每 15 秒输出当前自然日、父场景进度、已发现/完成 submission、Report 数、吞吐、
in-flight、失败数和 ETA。自然日仍然串行，只有日终完整校验通过才推进 checkpoint。

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
export SEEDDATA_HISTORICAL_BATCH_ID=hist-20250101-20260727-v1
export SEEDDATA_HISTORICAL_RESUME=1
# 仅第一次把旧批次迁移为 v2 时必填；迁移成功后可取消。
export SEEDDATA_HISTORICAL_LEGACY_SUBMISSION_FILE=/opt/seeddata-runner/.seeddata-cache/daily-simulation-submissions.json

./scripts/run_historical_container.sh
```

镜像内进程使用 UID/GID `10001:10001`；配置文件必须允许该用户读取，状态目录必须允许该
用户写入。脚本会用同一镜像用户实际创建并删除一个预检文件，不能写时会在回填前退出。
第一次 `--resume` 若尚无 v2 数据库，脚本还会把旧 submission ledger 只读挂载给迁移器；
缺少该文件时拒绝启动，避免遗漏已经 accepted/pending 的 AnswerSheet 身份。

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
SEEDDATA_HISTORICAL_LEGACY_SUBMISSION_FILE
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
  --batch-id hist-20250101-20260727-v1 \
  --expected-database "$QS_DB_NAME" \
  > /secure/path/hist-20250101-20260727-v1.testee-time-repair.sql
```

检查 SQL 中的数据库、Org、Testee 数量和时间范围，然后显式确认并执行：

```bash
mysql --defaults-extra-file="$QS_MYSQL_CNF" \
  --init-command="SET @qs_testee_time_repair_confirm='REPAIR_HISTORICAL_TESTEE_CREATED_AT'" \
  "$QS_DB_NAME" \
  < /secure/path/hist-20250101-20260727-v1.testee-time-repair.sql
```

SQL 只更新 manifest 明确归属本批次的 Testee ID，数据库、Org 或行数不匹配时会在事务前拒绝。
修复命令可幂等重跑，并显式保留原 `updated_at`。确认报到日期分布正确后，才使用同一批次执行
`historical-backfill --resume`。

## 只读检查

```bash
tmp/bin/seeddata historical-verify \
  --config configs/seeddata.yaml \
  --state-dir /secure/path/seeddata-historical-state \
  --batch-id hist-20250101-20260727-v1

tmp/bin/seeddata historical-manifest \
  --state-dir /secure/path/seeddata-historical-state \
  --batch-id hist-20250101-20260727-v1
```

`historical-verify` 同时检查本地 checkpoint/manifest 和 qs-server stage ledger；需要
Assessment 的场景必须存在 Assessment、Outcome、Report，Plan 子场景必须存在 task open/complete。

## 后续和失败处理

runner 完成后，按 qs-server 运行手册执行 19 个 Statistics repair/validate 窗口、最终 publish
和精确 Statistics 对账。失败时保留 `.seeddata-cache/historical`、服务端 stage ledger 和日志。
需要回滚时，先停 runner、worker、Plan scheduler，使用 manifest 和 qs-server 批次回滚工具；不得按 Org
或模糊时间范围清理。
