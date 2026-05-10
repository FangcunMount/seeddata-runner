# Seeddata Runner

`seeddata-runner` 是一个独立的 Go module，用来为 QS 环境持续制造更接近真实使用轨迹的 seed data。它不再提供按 step 执行的一次性脚本，而是由一个 supervisor 进程并发托管两条常驻 daemon：

- `daily_simulation_daemon`：每天生成一批模拟用户，完成注册、建档、绑定量表入口、加入 plan、填写答卷等流程。
- `plan_submit_open_tasks_daemon`：持续扫描指定 plan 下已经进入 `opened` 状态的任务，并代替用户提交答卷。

这个仓库只负责“模拟用户行为”和“补齐已打开任务的答卷提交”。plan 的创建、调度、打开、过期等生命周期仍由业务侧其他服务负责。

## 运行模型

`cmd/seeddata` 的启动流程固定如下：

1. 解析 CLI 参数，只支持 `--config` 和 `--verbose`
2. 加载 `seeddata.yaml`
3. 初始化 API client / collection client
4. 优先使用 `api.token`；如果为空，则使用 IAM 凭据换取 token
5. 并发启动 daily simulation 与 plan submit 两条 daemon

两个 daemon 共享同一个进程。任意一条退出报错，supervisor 就会退出；收到 `SIGINT` / `SIGTERM` 时会整体停止。

## 快速开始

先准备配置文件：

- 复制或修改 [configs/seeddata.yaml](./configs/seeddata.yaml)
- 填好 `api.baseUrl`
- 选择一种认证方式：
  - 直接提供 `api.token`
  - 或提供 `iam.*` 凭据，并让 runner 启动时自动换取 token

推荐通过脚本启动：

```bash
cd seeddata-runner
./scripts/run_seeddata_daemon.sh
```

脚本支持以下环境变量覆盖：

- `SEEDDATA_CONFIG`：配置文件路径，默认 `./configs/seeddata.yaml`
- `SEEDDATA_GO`：Go 可执行文件，默认 `go`
- `SEEDDATA_LOG_FILE`：日志文件路径，默认 `./logs/seeddata-daemon.log`

也可以直接运行：

```bash
cd seeddata-runner
go run ./cmd/seeddata --config ./configs/seeddata.yaml --verbose
```

`--verbose` 会把日志级别提升到 `debug`。

## 编译二进制并通过 systemd 运行

如果 `seeddata-runner` 已经以 `seeddata-runner.service` 的形式托管在服务器上，推荐把可执行文件编译成固定二进制，再交给 `systemd` 启停，而不是在线上直接 `go run`。

### 1. 编译二进制

在仓库根目录执行：

```bash
cd seeddata-runner
mkdir -p ./bin
go build -o ./bin/seeddata ./cmd/seeddata
```

如果需要交叉编译 Linux AMD64：

```bash
cd seeddata-runner
mkdir -p ./bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ./bin/seeddata-linux-amd64 ./cmd/seeddata
```

### 2. 确认 systemd 当前使用的启动方式

先检查服务单元文件，确认它当前是直接跑二进制，还是仍然在跑 `go run`：

```bash
sudo systemctl cat seeddata-runner.service
```

重点关注：

- `ExecStart`
- `WorkingDirectory`
- `Environment`
- `EnvironmentFile`

如果 `ExecStart` 还是类似：

```ini
ExecStart=/usr/bin/env bash -lc 'go run ./cmd/seeddata --config ./configs/seeddata.yaml'
```

建议改成固定二进制，例如：

```ini
ExecStart=/opt/seeddata-runner/bin/seeddata --config /opt/seeddata-runner/configs/seeddata.yaml
WorkingDirectory=/opt/seeddata-runner
EnvironmentFile=-/etc/seeddata-runner.env
```

这样升级时只需要替换二进制，不需要依赖线上 Go 编译环境。

### 3. 替换二进制

下面假设服务实际使用的二进制路径是 `/opt/seeddata-runner/bin/seeddata`。如果你的 unit file 使用的是别的路径，以 `ExecStart` 为准。

```bash
sudo install -d /opt/seeddata-runner/bin
sudo install -m 0755 ./bin/seeddata /opt/seeddata-runner/bin/seeddata
```

如果同时更新了配置文件，也一并覆盖对应路径。

### 4. 重新加载并重启服务

如果只替换二进制，不改 unit file，通常直接重启即可：

```bash
sudo systemctl restart seeddata-runner
```

如果修改了 unit file 或 `EnvironmentFile`，先 reload 再 restart：

```bash
sudo systemctl daemon-reload
sudo systemctl restart seeddata-runner
```

### 5. 验证服务是否按新版本运行

```bash
sudo systemctl status seeddata-runner --no-pager
sudo journalctl -u seeddata-runner -n 200 --no-pager
```

建议重点看这几类日志：

- `Fetching API token from IAM`
- `Initialized API client`
- `Daily simulation daemon started`
- `Plan opened-task answersheet daemon started`
- `Daily simulation daemon batch failed`

如果服务不断重启，可以继续检查：

```bash
sudo systemctl show seeddata-runner -p Environment
sudo systemctl cat seeddata-runner.service
```

这样可以快速确认：

- IAM 用户名/密码是否真的注入到了进程环境里
- `ExecStart` 是否已经切到新二进制
- `--config` 指向的是否是你预期的配置文件

### 6. 一次典型升级流程

```bash
cd /path/to/seeddata-runner
go build -o ./bin/seeddata ./cmd/seeddata
sudo install -m 0755 ./bin/seeddata
sudo systemctl restart seeddata-runner
sudo systemctl status seeddata-runner --no-pager
sudo journalctl -u seeddata-runner -n 100 --no-pager
```

如果这次升级还改了 unit file 或环境文件，则改为：

```bash
sudo systemctl daemon-reload
sudo systemctl restart seeddata-runner
```

## 认证与依赖

运行时依赖主要分为三类：

- `api.baseUrl`：业务 API 地址，必填
- `api.collectionBaseUrl`：采集/问卷相关 API 地址；为空时会回退到 `api.baseUrl`
- `iam.*`：当 `api.token` 为空时，用于登录并自动刷新 token；daily simulation 新建模拟 C 端账号时还会使用 IAM v2 内部 mock-consumer REST ensure

凭据优先级如下：

1. `api.token`
2. 环境变量 `IAM_USERNAME` / `IAM_PASSWORD`
3. 配置文件中的 `iam.username` / `iam.password`

如果 daily simulation 需要新建 guardian / child / testee，应启用 `iam.mockConsumer` 并配置 shared secret。IAM v2 已不再通过 AuthN gRPC 暴露 password account onboarding；未启用 mock-consumer 时仅能复用已经存在且可密码登录的 IAM 用户。

## 配置总览

配置结构固定为五段：

| 段落 | 作用 |
| --- | --- |
| `global` | 默认机构 ID、默认标签前缀 |
| `api` | 业务 API、采集 API、重试策略、静态 token |
| `iam` | IAM 登录、mock-consumer 建号与可选 gRPC 配置 |
| `dailySimulation` | 每日模拟用户生成策略 |
| `planSubmit` | opened task 答卷提交策略 |

其中 `dailySimulation` 和 `planSubmit` 是必填段；`api.baseUrl` 也是运行时硬要求。

## Daily Simulation

`dailySimulation` 用于构造“像真实用户漏斗一样”的新增数据，核心行为如下：

- 按计划时间触发批量生成
- 每个用户会确定性地选择一个 plan 和一个 journey 目标
- 旅程可在注册、建 testee、解析入口、提交答卷几个阶段提前停止
- 成功批次会写入 `stateFile`，用于限制每日总量并避免重复执行已完成的 slot

关键字段如下：

| 字段 | 说明 |
| --- | --- |
| `countPerRun` | 固定每轮生成数量；当 `countMin` / `countMax` 都为 0 时生效 |
| `countMin` / `countMax` | 每轮随机生成数量区间；设置后优先于 `countPerRun` |
| `dailyMaxUsers` | 当天成功新增用户上限；`<= 0` 表示不限制 |
| `workers` | 并发 worker 数 |
| `runAt` | 兼容单次调度模式，每天固定时刻跑一次 |
| `windowStartAt` / `windowEndAt` / `interval` | 窗口调度模式，在时间窗口内按固定间隔反复触发 |
| `retryDelay` | 一轮失败后的重试等待时间 |
| `stateFile` | daemon 状态文件，默认 `.seeddata-cache/daily-simulation-daemon-state.json` |
| `clinicianIds` | 可用医生范围；当 `entryId` 为空时必填 |
| `focusCliniciansPerRunMin` / `focusCliniciansPerRunMax` | 每轮从 `clinicianIds` 中抽取多少位医生参与本轮模拟；不配时默认使用全部医生 |
| `entryId` | 指定现成 assessment entry；设置后优先复用这个入口，并忽略 `clinicianIds` 选入口的逻辑 |
| `targetType` / `targetCode` / `targetVersion` | 当未指定 `entryId` 时，用于定位或创建 assessment entry；每个 testee 仍然只拿这一个入口 |
| `additionalTargetCodes` / `additionalTargetMaxCount` | 入口填完后额外填报的量表池，以及每个 testee 从池子里随机抽取 `1..additionalTargetMaxCount` 个量表；`additionalTargetMaxCount=0` 表示不额外填报 |
| `planIds` | 必填；每个模拟用户会从这里确定性选一个 plan 加入 |
| `journeyMix` | 控制四种旅程深度的权重分布 |
| `userPassword` / `userPhonePrefix` / `userEmailDomain` | 模拟 guardian 账号生成规则 |
| `guardianRelation` / `testeeSource` / `testeeTags` / `isKeyFocus` | 创建 testee 时写入的业务属性；`guardianRelation` 需使用 IAM 当前词表 `self / parent / grandparent / other`，legacy `guardian` 会自动归一化为 `other` |

`journeyMix` 支持四种目标：

- `registerOnlyWeight`
- `createTesteeWeight`
- `resolveEntryWeight`
- `submitAnswerWeight`

如果四项权重都不填，默认全部走 `submitAnswerWeight=100`。

当 `entryId` 为空时，runner 会：

1. 从本轮选中的 clinician 范围里寻找匹配 `targetType + targetCode + targetVersion` 的 assessment entry
2. 若找到了但已停用，则自动重新激活
3. 若没有找到，则自动创建一个新 entry

## Plan Submit

`planSubmit` 只处理当天“已经 opened 的任务”，不会参与 plan 调度本身。它会先按 `planned_at <= 当天 23:59:59` 拉取任务窗口，再在本地只保留当天任务；随后按 task 的 `testee_id` 查询 testee 来源，只提交 `source == dailySimulation.testeeSource` 的任务，并用 `completionPercent` 控制当天最多完成的比例。

关键字段如下：

| 字段 | 说明 |
| --- | --- |
| `planIds` | 必填；持续扫描这些 plan |
| `workers` | 并发提交 opened task 的 worker 数 |
| `completionPercent` | 当天 task 目标完成比例，取值 `0..100`；默认 `100` 保持旧行为，`0` 表示不提交 |
| `idleInterval` | 本轮没有活跃 opened task 时，下次轮询等待时间 |
| `activeInterval` | 本轮发现 opened task 并执行提交后，下次轮询等待时间 |

`planSubmit` 会复用 `dailySimulation.testeeSource` 作为安全边界，避免自动完成正常业务渠道创建的 testee 任务；如果某个 task 的 testee 来源查询失败，会跳过该 task。

启动时，每个 plan 会预先加载一次 plan、scale 和 questionnaire 元数据，之后进入连续轮询。

## 最小示例

下面是一份保留关键字段的最小配置骨架：

```yaml
global:
  orgId: 1

api:
  baseUrl: "https://qs.example.com"
  collectionBaseUrl: "https://collect.example.com"
  token: ""

iam:
  baseUrl: "https://iam.example.com"
  loginUrl: "https://iam.example.com/api/v2/authn/login"
  username: ""
  password: ""
  tenantId: "1"
  mockConsumer:
    enabled: true
    sharedSecret: "replace-with-iam-seed-secret"
    endpointPath: "/api/v2/internal/authn/mock-consumers/ensure"
    maxConcurrent: 1
  grpc:
    address: "iam-apiserver:9090"

dailySimulation:
  countPerRun: 20
  dailyMaxUsers: 120
  workers: 6
  windowStartAt: "10:00"
  windowEndAt: "18:00"
  interval: "30m"
  retryDelay: "30m"
  clinicianIds:
    - "614995509882401326"
  targetType: "scale"
  targetCode: "3adyDE"
  additionalTargetCodes: []
  additionalTargetMaxCount: 0
  planIds:
    - "614333603412718126"
  journeyMix:
    submitAnswerWeight: 100

planSubmit:
  planIds:
    - "614333603412718126"
  workers: 1
  completionPercent: 50
  idleInterval: "30s"
  activeInterval: "5s"
```

完整示例见 [configs/seeddata.yaml](./configs/seeddata.yaml)。

## CLI 约束

当前 CLI 明确只有一个 supervisor 入口，不再支持历史上的 step 式执行：

- 支持 `--config`
- 支持 `--verbose`
- 不支持 `--steps`
- 不支持按单个 seed step 运行
- 不再包含历史 backfill / fixup 工具入口

## 项目结构

主要目录如下：

- [cmd/seeddata](./cmd/seeddata)：唯一进程入口与 supervisor
- [configs](./configs)：配置样例
- [scripts](./scripts)：启动脚本
- [internal/dailysim](./internal/dailysim)：每日模拟用户 daemon
- [internal/plansubmit](./internal/plansubmit)：opened task 提交 daemon
- [internal/seedconfig](./internal/seedconfig)：配置加载、默认值、校验
- [internal/seedruntime](./internal/seedruntime)：日志、signal、client 初始化
- [internal/seedapi](./internal/seedapi) / [internal/seediauth](./internal/seediauth)：API 与 IAM 访问封装

## 开发与验证

常用命令：

```bash
go test ./...
```

如果只想检查 CLI 或配置解析，也可以从这些文件开始：

- [cmd/seeddata/main.go](./cmd/seeddata/main.go)
- [internal/seedconfig/config.go](./internal/seedconfig/config.go)
- [scripts/run_seeddata_daemon.sh](./scripts/run_seeddata_daemon.sh)
