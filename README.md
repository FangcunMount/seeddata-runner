# Seeddata Runner

`seeddata-runner` 是 QS 环境的每日模拟服务。一个 supervisor 进程并发托管两条常驻 daemon：

- `daily_simulation_daemon`：只使用进程所在时区的当前日期，创建 Guardian、Testee、Assessment Entry 访问关系、Plan enrollment，并通过普通 collection 接口提交 AnswerSheet。
- `plan_submit_open_tasks_daemon`：查询指定 Plan 当天已经进入 `opened` 的任务，通过普通 admin submit 接口幂等提交答卷。

runner 不创建 Plan，也不推进 Plan 的 schedule/open/expire 生命周期。

## CLI

可执行程序只接受两个参数：

```text
--config <path>   配置文件，默认 ./configs/seeddata.yaml
--verbose         开启 debug 日志
```

位置参数、未知参数和其他命令都会立即返回 CLI 错误。配置文件采用严格 YAML 字段解析，未知字段会导致启动失败。

## 启动

推荐通过现有脚本启动：

```bash
./scripts/run_seeddata_daemon.sh
```

脚本支持：

- `SEEDDATA_CONFIG`：配置文件路径。
- `SEEDDATA_GO`：Go 可执行文件。
- `SEEDDATA_LOG_FILE`：日志文件路径。

也可以直接运行：

```bash
go run ./cmd/seeddata --config ./configs/seeddata.yaml --verbose
```

两个 daemon 共享进程生命周期：任意一条异常退出都会结束 supervisor；`SIGINT` 和 `SIGTERM` 会停止整个进程。

## 认证和环境变量

业务 API token 优先读取 `api.token`；为空时使用 IAM 登录配置换取并自动刷新 token。

可用环境变量：

- `IAM_USERNAME`
- `IAM_PASSWORD`
- `IAM_MOCK_CONSUMER_SHARED_SECRET`
- `SEEDDATA_API_BASE_URL`
- `SEEDDATA_COLLECTION_BASE_URL`
- `SEEDDATA_IAM_BASE_URL`
- `SEEDDATA_IAM_LOGIN_URL`
- `SEEDDATA_DAILY_SUBMISSION_STATE_FILE`

新建模拟 Guardian 时应启用 `iam.mockConsumer`。shared secret 不应提交到仓库，应通过 `IAM_MOCK_CONSUMER_SHARED_SECRET` 或部署密钥注入。

## 配置

配置固定为五段：

| 段落 | 作用 |
| --- | --- |
| `global` | 机构 ID 和默认标签 |
| `api` | qs-server、collection-server、token 和重试 |
| `iam` | IAM 登录、mock-consumer 和可选 gRPC |
| `dailySimulation` | 当前日期的每日模拟 |
| `planSubmit` | 当天 opened task 的幂等提交 |

完整示例见 [configs/seeddata.yaml](./configs/seeddata.yaml)。

### dailySimulation

调度支持：

- 单时刻：设置 `runAt`。
- 时间窗口：设置 `windowStartAt`、`windowEndAt`、`interval`。
- `dailyMaxUsers` 限制当天成功用户总数。
- 如果进程在窗口结束后启动，只复用当前日期已存在且身份匹配的 Testee 做一次 catch-up，不创建其他日期的数据。
- `countMin` / `countMax` 决定每轮数量；两者均为 0 时使用 `countPerRun`。
- `workers` 只控制单轮普通用户并发。

`journeyMix` 用相对权重选择停止位置：

- `registerOnlyWeight`
- `createTesteeWeight`
- `resolveEntryWeight`
- `submitAnswerWeight`

`additionalTargetCodes` 和 `additionalTargetMaxCount` 可在主入口完成后追加普通 self-service Questionnaire；追加目标按顺序提交。

### Plan enrollment 与 planSubmit

每日完整旅程会调用普通 Plan enroll 接口，并记录返回的 `enrollment_id` 和任务 ID。任务提交仍由独立的 `plan_submit_open_tasks_daemon` 完成。

`planSubmit` 只处理：

- 配置中指定的 Plan。
- 当前日期窗口内的任务。
- 状态为 `opened` 的任务。
- Testee source 与 `dailySimulation.testeeSource` 一致的任务。

`completionPercent` 可限制当天完成比例；设为 `0` 可在验收时暂停任务提交而不停止 daemon。

## 每日 AnswerSheet 闭环

需要测评的提交只有完成以下步骤才算成功：

1. 在 JSON 账本中持久化 logical ID、payload fingerprint、稳定 idempotency key 和 request ID。
2. POST collection AnswerSheet；accepted 后立即持久化 `answersheet_id`。
3. 查询 assessment readiness；ready 后立即持久化 `assessment_id`，账本状态为 `ready`。
4. 查询 report wait；只有 `status=interpreted` 才完成本次旅程，并在日志中写入 `report_status=interpreted`。

异常语义：

- readiness 超时：账本保存为 `accepted_pending`，本轮返回错误。
- report pending/processing 超时或 failed：账本保持 `ready`，本轮返回错误。
- 重试会复用既有 idempotency key、AnswerSheet 和 Assessment，只继续缺失的查询，不重复 POST 答卷。
- 不需要测评的 Questionnaire 以 AnswerSheet durable accepted 为终点。

daily 与 plan 账本都采用原子临时文件、fsync 和 rename；已完成记录保留 30 天。旧 JSON 记录可直接加载，payload fingerprint 冲突会保持 sticky conflict。

## 使用的普通接口

runner 只调用以下现行接口职责：

- IAM mock-consumer ensure。
- Testee 创建和当前日期 Testee 查询。
- Assessment Entry 创建、resolve、intake、reactivate。
- Published Assessment Model 和 Published Questionnaire 查询。
- Plan 查询、enroll 和普通 task window。
- Collection AnswerSheet submit。
- Assessment readiness。
- Assessment report wait。
- Plan task admin submit。

## 构建与验证

本地门禁：

```bash
go mod verify
go vet ./...
go test ./...
go test -race ./internal/answersheet ./internal/dailysim ./internal/plansubmit ./internal/seedapi ./internal/seedruntime
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/seeddata ./cmd/seeddata
git diff --check
```

CI 还会运行 actionlint 和剩余 shell 脚本的 `bash -n`。

## systemd 部署

推荐部署固定二进制：

```ini
ExecStart=/opt/seeddata-runner/bin/seeddata --config /opt/seeddata-runner/configs/seeddata.yaml
WorkingDirectory=/opt/seeddata-runner
EnvironmentFile=-/etc/seeddata-runner.env
```

升级前先检查真实 unit：

```bash
sudo systemctl cat seeddata-runner.service
sudo systemctl show seeddata-runner.service -p EnvironmentFiles -p DropInPaths -p WorkingDirectory
```

替换二进制后：

```bash
sudo systemctl restart seeddata-runner
sudo systemctl status seeddata-runner --no-pager
sudo journalctl -u seeddata-runner -n 200 --no-pager
```

## 当前日期单用户验收

使用临时配置执行完整每日闭环时，建议：

- `TZ=Asia/Shanghai`
- `countMin=countMax=dailyMaxUsers=workers=1`
- `registerOnlyWeight=createTesteeWeight=resolveEntryWeight=0`
- `submitAnswerWeight=100`
- `additionalTargetCodes=[]`、`additionalTargetMaxCount=0`
- `planSubmit.completionPercent=0`
- 使用独立 `testeeSource`、phone prefix、email domain 和全新状态文件
- `runAt` 设置为当前日期的下一可执行分钟

成功日志应同时出现当前上海日期、Guardian user ID、Testee ID、Entry ID、Plan ID、enrollment ID、AnswerSheet ID、Assessment ID、`report_status=interpreted` 和 `stop_reason=completed`。
