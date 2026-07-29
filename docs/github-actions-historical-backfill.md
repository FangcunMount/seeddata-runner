# GitHub Actions 历史回填部署与操作手册

本文用于在 CI 已通过后，由操作人员手动把正式历史回填部署到 ServerA，并在不中断
`batch_id`、状态目录和幂等身份的前提下完成监控、停止与恢复。

## 先看结论

- `CI` 成功后**不会自动进入 CD**。历史回填会长期写入生产业务事实，因此部署只能手动触发。
- 正式批次固定为 `hist-20250101-20260727-v2`，日期固定为
  `2025-01-01..2026-07-27`，并且必须启用 `resume=true`。
- 部署 Action 只负责构建不可变镜像、上传到 ServerA 并启动 systemd unit；Action 成功不代表
  573 天回填已经完成。
- 回填由 `seeddata-historical-backfill.service` 继续托管。状态检查和停止使用独立的
  `Historical Backfill Control` workflow。
- IAM/QS 业务密钥只保存在 ServerA 的受限 env 文件中，不写入仓库，也不经过 GitHub Actions。
- 失败恢复优先重新运行原部署 run，以保持 commit SHA、配置、镜像和 workflow inputs 不变。

正式批次开始后冻结 seeddata-runner revision、`configs/seeddata.yaml`、Plan、Questionnaire 和
Model 版本。在回填和最终验收完成前，不要把普通 seeddata daemon、Plan scheduler 或新的目标版本
重新投入运行。

## 1. 三个 workflow 的职责

| Workflow | 触发方式 | 作用 | 是否等待 573 天完成 |
| --- | --- | --- | --- |
| `CI` | push/PR 到 `main` | 测试、race、部署契约、Linux 构建和历史镜像构建 | 不适用 |
| `Historical Backfill Deploy` | 仅 `workflow_dispatch` | 校验请求、构建并发布 SHA 镜像、在 ServerA 启动 systemd unit | 否 |
| `Historical Backfill Control` | 仅 `workflow_dispatch` | 查看状态，或安全停止 unit/容器 | 不适用 |

Deploy workflow 会再次执行全库测试、历史关键包 race 和部署契约，因此待部署 revision 会在发布前
再校验一次。镜像使用如下不可变标签：

```text
ghcr.io/fangcunmount/seeddata-runner-historical:<commit-sha>
```

GHCR 登录使用 workflow 自带的 `GITHUB_TOKEN` 和 `packages: write` 权限，不需要人工创建 GHCR
Token。

## 2. 首次配置 GitHub

### 2.1 创建 production Environment

在仓库 `FangcunMount/seeddata-runner` 打开：

```text
Settings → Environments → New environment → production
```

建议为 `production` 配置 Required reviewers。Deploy 的构建阶段可以先执行；真正连接 ServerA
的 `Start on ServerA` job 会等待 production 审批。Control 的 status/stop 也使用同一个
Environment。

### 2.2 配置 ServerA 连接项

以下配置可以来自 repository、organization 或 production Environment。Organization Secret
必须允许 `FangcunMount/seeddata-runner` 使用。

| 名称 | 类型 | 必需 | 说明 |
| --- | --- | --- | --- |
| `SVR_MINI_SSH_KEY` 或 `SVRA_SSH_KEY` | Secret | 是 | ServerA SSH 私钥；若两个都配置，前者优先 |
| `SVRA_PUBLIC_HOST` 或 `SVRA_HOST` | Variable/Secret | 是 | SSH 可达地址；只有 `SVRA_PUBLIC_HOST` 允许配置成 Secret |
| `SVRA_USERNAME` | Variable | 是 | SSH 用户 |
| `SVRA_SSH_PORT` | Variable | 否 | 默认 `22` |
| `SVRA_SSH_FINGERPRINT` | Variable/Secret | 是 | ServerA SSH Host Key 的 SHA256 指纹 |
| `SVRA_SUDO_PASSWORD` | Secret | 条件必需 | SSH 用户没有 NOPASSWD sudo 时使用 |

SSH 指纹必须从 ServerA 控制台直接读取，不能把首次网络扫描结果当作可信值：

```bash
for key_file in /etc/ssh/ssh_host_*_key.pub; do
  ssh-keygen -lf "$key_file" -E sha256
done
```

配置其中一个实际启用的 Host Key 指纹。workflow 会扫描目标主机并要求至少一个返回指纹与配置
精确一致；不一致时会拒绝 SSH。

可用 GitHub CLI 只读确认名称是否存在。Secret 只能看到名称，无法取回值：

```bash
gh variable list --repo FangcunMount/seeddata-runner
gh secret list --repo FangcunMount/seeddata-runner
gh variable list --repo FangcunMount/seeddata-runner --env production
gh secret list --repo FangcunMount/seeddata-runner --env production
```

### 2.3 配置可选生产路径

不配置时 workflow 使用下表默认值：

| production Variable | 默认值 |
| --- | --- |
| `SEEDDATA_HISTORICAL_STATE_DIR` | `/secure/path/seeddata-historical-state` |
| `SEEDDATA_HISTORICAL_ENV_FILE` | `/secure/path/seeddata-historical.env` |
| `SEEDDATA_HISTORICAL_BASELINE_FILE` | `/secure/path/hist-20250101-20260727-v1.baseline.json` |
| `SEEDDATA_HISTORICAL_DEPLOY_ROOT` | `/opt/seeddata-runner-historical` |
| `SEEDDATA_HISTORICAL_LOG_DIR` | `/secure/path/seeddata-historical-logs` |

如果实际文件不在默认路径，应修改对应 Variable；不要为了适配默认值而重新创建一份含义不同的
状态文件或 baseline。Baseline 文件名保留 `v1`，因为它是首次回填前已封存的原始零基线，
不是可恢复的 runner manifest 版本。

## 3. 准备 ServerA

以下步骤只需在第一次正式部署前完成一次；每次恢复仍要复核。

### 3.1 准备业务 Secret 文件

业务 Secret 不配置到 GitHub。先在 ServerA 创建文件，再使用 `sudoedit` 写入实际值：

```bash
sudo install -d -m 0700 /secure/path
sudo test -e /secure/path/seeddata-historical.env || \
  sudo install -m 0600 /dev/null /secure/path/seeddata-historical.env
sudoedit /secure/path/seeddata-historical.env
```

文件至少包含以下四项：

```dotenv
IAM_USERNAME=<tenant-1 下 active 的 QS 管理账号>
IAM_PASSWORD=<该账号密码>
IAM_MOCK_CONSUMER_SHARED_SECRET=<IAM mock-consumer sharedSecret>
QS_HISTORICAL_CONTEXT_SECRET=<apiserver 与 collection-server 使用的同一历史密钥>
```

`IAM_USERNAME` 必须能取得同时适用于 QS 和 Collection 的有效 JWT，并解析出正确的 Org。不要使用
runner 将要创建的 guardian 账号。

只检查键名和权限，不要输出值：

```bash
sudo stat -c '%a %U:%G %s %n' /secure/path/seeddata-historical.env

for required_key in \
  IAM_USERNAME \
  IAM_PASSWORD \
  IAM_MOCK_CONSUMER_SHARED_SECRET \
  QS_HISTORICAL_CONTEXT_SECRET; do
  sudo grep -Eq "^${required_key}=.+" /secure/path/seeddata-historical.env || {
    echo "missing: $required_key"
    exit 1
  }
done
```

权限必须是 `0400` 或 `0600`。

### 3.2 核对 baseline 与历史状态

baseline 和 checksum 必须已经存在：

```bash
BASELINE=/secure/path/hist-20250101-20260727-v1.baseline.json

sudo test -s "$BASELINE"
sudo test -s "${BASELINE}.sha256"
sudo sha256sum -c "${BASELINE}.sha256"
```

准备状态和日志目录。镜像中的 runner 使用 `10001:10001`：

```bash
sudo install -d -o 10001 -g 10001 -m 0700 \
  /secure/path/seeddata-historical-state
sudo install -d -m 0700 /secure/path/seeddata-historical-logs

sudo chown -R 10001:10001 /secure/path/seeddata-historical-state
sudo find /secure/path/seeddata-historical-state \
  -type d -exec chmod 0700 {} +
sudo find /secure/path/seeddata-historical-state \
  -type f -exec chmod 0600 {} +
```

这里只允许递归调整本批次专用的 state dir，不能把 `/secure/path` 或其他共享目录作为递归目标。

正式批次的 v2 数据库路径可这样确定：

```bash
BATCH_ID=hist-20250101-20260727-v2
BATCH_HASH="$(printf '%s' "$BATCH_ID" | sha256sum | awk '{print substr($1,1,16)}')"
V2_DB="/secure/path/seeddata-historical-state/${BATCH_HASH}/historical-state-v2.db"
printf 'v2 db: %s\n' "$V2_DB"
```

正式 v2 批次首次启动时，即使 `resume=true` 且数据库尚不存在，runner 也会创建全新状态，不再
导入 v1 submission ledger。Manifest v1 缺少冻结 Profile，会在任何 IAM 登录或业务 HTTP 请求前
被拒绝；必须先完成 v1 远端 mock 数据清理，再使用新的 v2 batch。首次启动前确认 v2 数据库确实
不存在：

```bash
if sudo test -e "$V2_DB"; then
  echo "ERROR: v2 state already exists; verify whether this is a resume" >&2
  exit 1
fi
```

### 3.3 核对运行依赖

回填开始前应满足：

- IAM 已启动，QS 启动日志显示 Token Verifier 初始化成功。
- `qs-apiserver`、`qs-collection-server` 和 `iam-apiserver` 已加入 `infra-network`。
- qs-server migration 已到当前要求版本；本批次使用的 `seed_backfill_stage`/attempt 表存在。
- apiserver 和 collection-server 使用相同的 `QS_HISTORICAL_CONTEXT_SECRET`。
- 历史能力只允许目标 Org 和 `2025-01-01..2026-07-27`。
- apiserver 的 Plan scheduler 已暂停，若有独立 Plan scheduler 也已停止。
- Evaluation、Interpretation、Outbox/Worker 保持运行，能够生成 Outcome 和 Report。
- 普通 `seeddata-runner.service` 已停止。

ServerA 上执行：

```bash
sudo systemctl stop seeddata-runner.service
sudo systemctl is-active seeddata-runner.service || true

sudo docker network inspect infra-network >/dev/null
sudo docker exec qs-apiserver getent hosts iam-apiserver
sudo docker exec qs-apiserver getent hosts qs-collection-server
```

普通 seeddata daemon 若仍为 `active`，Deploy 会在启动任何历史容器前拒绝执行。

## 4. 冻结并确认待部署 revision

正式运行前记录 `main` 的 SHA，并确认该 SHA 的 `CI` 已成功：

```bash
cd /path/to/seeddata-runner
git fetch origin main
git switch main
git pull --ff-only origin main

DEPLOY_REVISION="$(git rev-parse HEAD)"
printf 'deploy revision: %s\n' "$DEPLOY_REVISION"

gh run list \
  --repo FangcunMount/seeddata-runner \
  --workflow ci.yml \
  --commit "$DEPLOY_REVISION" \
  --limit 5
```

确认 `Run Tests`、`Historical Backfill Race Tests`、`Deployment Contracts` 和 `Linux Build`
都通过。建议在正式回填期间冻结 seeddata-runner 的 `main` 或至少冻结本次 revision；新文档提交也会
产生新 SHA，不应在恢复时无意中切换镜像。

再检查该 revision 中的生产配置：

```bash
git show "$DEPLOY_REVISION:configs/seeddata.yaml" | less
```

重点核对 Org、人数范围、Plan ID、入口、target code/version、并发和进度间隔。Deploy 打包的是
该 revision 内的 `configs/seeddata.yaml`，不会读取 ServerA 上另一份手工配置。

## 5. 手动启动 Deploy

### 5.1 从网页启动

打开：

```text
FangcunMount/seeddata-runner → Actions → Historical Backfill Deploy → Run workflow
```

输入固定值：

| 输入 | 值 |
| --- | --- |
| Use workflow from | `main` |
| `confirmation` | `START_HISTORICAL_BACKFILL` |
| `batch_id` | `hist-20250101-20260727-v2` |
| `from` | `2025-01-01` |
| `to` | `2026-07-27` |
| `resume` | `true` |

正式批次只接受上述日期和 `resume=true`。提交后等待 `production` Environment 审批，然后确认
`Start on ServerA` job 成功。

### 5.2 使用 GitHub CLI 启动

```bash
gh workflow run historical-deploy.yml \
  --repo FangcunMount/seeddata-runner \
  --ref main \
  -f confirmation=START_HISTORICAL_BACKFILL \
  -f batch_id=hist-20250101-20260727-v2 \
  -f from=2025-01-01 \
  -f to=2026-07-27 \
  -f resume=true
```

查找刚触发的 run，然后使用显示出的 ID 监控：

```bash
gh run list \
  --repo FangcunMount/seeddata-runner \
  --workflow historical-deploy.yml \
  --event workflow_dispatch \
  --limit 5

gh run watch <deploy-run-id> \
  --repo FangcunMount/seeddata-runner \
  --exit-status
```

失败时查看失败 job 日志：

```bash
gh run view <deploy-run-id> \
  --repo FangcunMount/seeddata-runner \
  --log-failed
```

### 5.3 Deploy 成功实际代表什么

workflow 完成了以下工作：

1. 校验分支、确认词、批次、日期和 `resume`。
2. 对准确 revision 重新运行测试和部署契约。
3. 构建 `linux/amd64` 镜像并用 commit SHA 发布到 GHCR。
4. 生成镜像 tar、部署包和 SHA256 校验文件。
5. 通过固定 SSH Host Key 连接 ServerA，并确认实际 hostname 为 `serverA`。
6. 校验 Secret 文件、baseline/checksum、v2 状态目录和普通 daemon 状态。
7. 校验 `infra-network`、容器 DNS 和三个内部健康接口。
8. 安装 revision、systemd unit 和部署 receipt，启动历史容器。

Action 在 unit 和容器连续稳定运行至少 15 秒后退出。若启动期间失败，部署日志会同时输出 systemd、journal 和 runner append 日志。573 天任务仍在 ServerA 后台运行。

## 6. 运行中监控

### 6.1 使用 Control workflow 查看状态

网页打开：

```text
Actions → Historical Backfill Control → Run workflow
```

输入：

```text
operation=status
batch_id=hist-20250101-20260727-v2
confirmation=<留空>
```

或使用 CLI：

```bash
gh workflow run historical-control.yml \
  --repo FangcunMount/seeddata-runner \
  --ref main \
  -f operation=status \
  -f batch_id=hist-20250101-20260727-v2
```

输出包含 systemd 状态、容器状态、最近 100 行 journal、最近 100 行 runner 日志和部署 receipt。

### 6.2 在 ServerA 实时查看

```bash
sudo systemctl show seeddata-historical-backfill.service \
  --property=LoadState,ActiveState,SubState,Result,ExecMainPID,ExecMainStatus \
  --no-pager

sudo docker ps -a \
  --filter 'name=seeddata-historical-hist-20250101-20260727-v2'

sudo tail -F \
  /secure/path/seeddata-historical-logs/hist-20250101-20260727-v2.log
```

正常运行时每 15 秒会出现日期、父场景、submission、Report、吞吐、in-flight、失败数和 ETA。
自然日仍串行推进；只有当天完整验证通过才会推进 checkpoint。

部署 receipt 用于确认 revision 和状态目录：

```bash
sudo sed -n \
  -e '/^revision=/p' \
  -e '/^image=/p' \
  -e '/^batch_id=/p' \
  -e '/^from=/p' \
  -e '/^to=/p' \
  -e '/^resume=/p' \
  -e '/^state_dir=/p' \
  -e '/^deployed_at=/p' \
  /opt/seeddata-runner-historical/deployment-hist-20250101-20260727-v2.txt
```

### 6.3 在 QS MySQL 查看服务端完成事实

`seed_backfill_stage.business_at` 是历史业务时间；`created_at` 是系统实际写入时间。两者不同是
预期行为，不表示 runner 先创建了“今天的业务数据”。

按业务日观察主要阶段：

```sql
SELECT
  DATE(business_at) AS business_date,
  COUNT(DISTINCT scenario_id) AS scenario_count,
  SUM(stage = 'answersheet_submit') AS answersheet_count,
  SUM(stage = 'outcome_committed') AS outcome_count,
  SUM(stage = 'report_generated') AS report_count
FROM seed_backfill_stage
WHERE org_id = 1
  AND batch_id = 'hist-20250101-20260727-v2'
GROUP BY DATE(business_at)
ORDER BY business_date DESC
LIMIT 14;
```

查看最近失败 attempt：

```sql
SELECT
  scenario_id,
  stage,
  attempt_no,
  status,
  resource_type,
  resource_id,
  LEFT(error_text, 240) AS error_text,
  started_at,
  finished_at
FROM seed_backfill_stage_attempt
WHERE org_id = 1
  AND batch_id = 'hist-20250101-20260727-v2'
  AND status = 'failed'
ORDER BY finished_at DESC
LIMIT 50;
```

不要仅凭 stage 总数宣布完成；日级 checkpoint、本地 manifest、服务端完成事实和最终
`historical-verify` 必须一致。

## 7. 安全停止

停止只终止 systemd unit 和历史容器，不删除 bbolt、manifest、日志、IAM 用户或 QS 业务事实。

网页运行 `Historical Backfill Control`：

```text
operation=stop
batch_id=hist-20250101-20260727-v2
confirmation=STOP_HISTORICAL_BACKFILL
```

或使用 CLI：

```bash
gh workflow run historical-control.yml \
  --repo FangcunMount/seeddata-runner \
  --ref main \
  -f operation=stop \
  -f batch_id=hist-20250101-20260727-v2 \
  -f confirmation=STOP_HISTORICAL_BACKFILL
```

停止后再执行一次 `status`，确认 `ActiveState=inactive` 且对应容器不再运行。不要手工删除状态目录
来“重置”任务。

## 8. 失败后的恢复

恢复遵循以下顺序：

1. 执行 `status`，保存失败日志、部署 run ID、receipt revision 和当前状态目录备份。
2. 根据错误修复外部原因，例如 IAM/JWKS、Worker、网络、磁盘、版本漂移或权限。
3. 若 unit/容器仍运行但已无法推进，先使用 Control workflow 安全停止。
4. 确认 batch ID、日期、state dir、Secret、Plan 和 target 冻结版本均未变化。
5. 重新运行**原来的 Deploy run**，让 GitHub 继续使用原 event SHA 和原 inputs。

使用原 deploy run ID：

```bash
gh run rerun <original-deploy-run-id> \
  --repo FangcunMount/seeddata-runner

gh run watch <original-deploy-run-id> \
  --repo FangcunMount/seeddata-runner \
  --exit-status
```

也可以在原 run 页面选择 `Re-run all jobs`。只有在确认当前 `main` SHA 与 receipt 中的 revision
相同，或者新 revision 已经过兼容性审查时，才允许新建一次 workflow dispatch。

`--resume` 会：

- 复用同一个 bbolt 状态和原 idempotency key。
- 从第一个真正缺失的阶段继续。
- 以 qs-server `seed_backfill_stage` 为历史业务完成权威。
- 对 accepted/pending 的 AnswerSheet 使用原 ID 继续轮询。
- 已完成阶段只做校验或本地补账，不重复制造业务事实。

不要通过更换 batch ID、删除本地记录或生成新幂等键绕过 conflict。

## 9. 常见失败

| 现象 | 检查与处理 |
| --- | --- |
| Deploy 等待不动 | 检查 production Environment 是否等待 reviewer，以及 qlume runner 是否在线 |
| `SEEDDATA_SSH_* is required` | 检查 Secret/Variable 名称、作用域和组织 Secret 的 repository access |
| `ServerA SSH fingerprint mismatch` | 从 ServerA 控制台重新读取实际 Host Key；先确认是否正常轮换，禁止直接接受网络扫描值 |
| GHCR push/pull 失败 | 检查 Actions `packages: write` 权限和仓库/组织 Package policy；不需要另配 PAT |
| baseline 缺失或 checksum 失败 | 使用原 baseline 和原 `.sha256`；查明文件变化，禁止重新捕获零基线覆盖旧文件 |
| Secret 文件权限错误 | 改为 `0400` 或 `0600`，并确认四个必需键非空 |
| `ordinary seeddata-runner.service is active` | 停止普通 daemon，再重新运行原 Deploy run |
| 状态目录不可写 | 确认目录存在且镜像用户 `10001:10001` 可写 |
| `historical manifest version 1 is not resumable` | 停止 v1 恢复；完成旧批次远端数据清理后，以正式 v2 batch 创建全新状态 |
| `infra-network`、DNS 或 health 失败 | 先恢复三个服务和容器网络；部署不会回退到公网 API |
| IAM 登录后 QS 返回 403 | 确认 IAM 在 QS 启动前可用、QS Token Verifier 已注入、JWT 含正确 Org |
| Report/Outcome 长时间缺失 | 检查 Evaluation/Interpretation Worker、Mongo/MySQL Outbox 与队列积压；当天不会写 checkpoint |
| payload/version conflict | 立即停止，比较冻结配置、manifest 和服务端 stage；不能换 ID 继续 |
| unit `Result=failed` | 先看 runner 日志和 failed attempt，修复后按第 8 节用原 revision 恢复 |

以下任一情况应立即停止，不进入下一天：

- payload、版本、业务时间或资源 ID conflict。
- 必需的 task complete、Assessment、Outcome 或 Report 阶段缺失。
- Report 未 generated 或 Worker/Outbox 持续失败。
- QS 队列持续增长、429/5xx 明显增加或延迟失控。
- 本地 ledger、manifest 和服务端 stage 无法对齐。

## 10. 判断 runner 是否完成

systemd 进程正常结束时应看到：

```text
ActiveState=inactive
SubState=dead
Result=success
ExecMainStatus=0
```

这仍只是进程级结果。还必须使用部署镜像执行只读验证。先从 receipt 取得准确 image revision，再
在 ServerA 运行；以下 `IMAGE_REF` 必须替换为 receipt 中的完整值：

```bash
IMAGE_REF='ghcr.io/fangcunmount/seeddata-runner-historical:<receipt-revision>'

sudo docker run --rm \
  --network infra-network \
  --read-only \
  --cap-drop=ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --env-file /secure/path/seeddata-historical.env \
  --env SEEDDATA_API_BASE_URL=http://qs-apiserver:8080 \
  --env SEEDDATA_COLLECTION_BASE_URL=http://qs-collection-server:8080 \
  --env SEEDDATA_IAM_BASE_URL=http://iam-apiserver:9080 \
  --env SEEDDATA_IAM_LOGIN_URL=http://iam-apiserver:9080/api/v2/authn/login \
  --mount type=bind,src=/opt/seeddata-runner-historical/current/configs/seeddata.yaml,dst=/run/seeddata/config.yaml,readonly \
  --mount type=bind,src=/secure/path/seeddata-historical-state,dst=/state \
  "$IMAGE_REF" \
  historical-verify \
  --config /run/seeddata/config.yaml \
  --state-dir /state \
  --batch-id hist-20250101-20260727-v2
```

命令必须退出 `0`，且 JSON 中 `complete=true`。再导出最终 manifest 并保存 checksum：

```bash
umask 077
MANIFEST_TMP="$(mktemp)"

sudo docker run --rm \
  --read-only \
  --cap-drop=ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --mount type=bind,src=/secure/path/seeddata-historical-state,dst=/state \
  "$IMAGE_REF" \
  historical-manifest \
  --state-dir /state \
  --batch-id hist-20250101-20260727-v2 \
  > "$MANIFEST_TMP"

sudo install -m 0600 "$MANIFEST_TMP" \
  /secure/path/hist-20250101-20260727-v2.manifest.json
rm -f "$MANIFEST_TMP"

sudo sha256sum /secure/path/hist-20250101-20260727-v2.manifest.json |
  sudo tee /secure/path/hist-20250101-20260727-v2.manifest.json.sha256 >/dev/null
```

## 11. Statistics、K6 与收尾

runner 完成后切换到 qs-server 仓库，按照其
`scripts/oneoff/HISTORICAL_BACKFILL_RUNBOOK.md` 执行：

1. `rebuild_statistics --mode historical-backfill`：19 个历史窗口逐个 repair/validate，并 catch-up
   到执行时最新完整上海自然日，最后 publish。
2. `verify_historical_statistics --mode verify`：使用回填前 baseline 对本批次精确资源进行逐日对账。
3. `make perf-preflight` 和 `make perf-smoke`：必须自动发现真实 Report 样本，不能是
   `no-report degraded`。

全部通过后：

- 关闭 apiserver/collection-server 的历史开关。
- 恢复 Plan scheduler 和普通 seeddata daemon。
- 轮换并移除一次性 `QS_HISTORICAL_CONTEXT_SECRET`。
- 保留 baseline、v2 bbolt/manifest、stage/attempt、部署 receipt、日志和验证输出至少
  90 天。

## 12. 禁止事项

- 不把 CI 成功配置成自动生产回填。
- 不在正式批次中更换日期、batch ID、state dir 或 idempotency key。
- 不删除正在运行或待验收的 v2 `historical-state-v2.db`、manifest 或 checkpoint。
- 不在恢复时随意切换 seeddata revision 或 `configs/seeddata.yaml`。
- 不在批次运行中发布或修改冻结的 Plan、Questionnaire、Model。
- 不在历史任务运行时启动普通 seeddata daemon 或 Plan scheduler。
- 不把 IAM/QS 业务 Secret 放入 YAML、workflow 日志、部署包或 GitHub Artifact。
- 不因为 `created_at` 是执行当天，就把历史 `business_at` 判断成错误。
- 不在 runner 验证、Statistics 对账和 K6 验收前关闭历史能力或删除账本。
