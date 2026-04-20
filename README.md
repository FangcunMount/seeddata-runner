# QS Seeddata Runner

`seeddata-runner` 是和 `qs-server`、`iam-contracts` 同层的独立 Go module，用来承载 mock 用户生成和 plan open-task 提交。

进程启动后会并发运行两条后台流水：

1. `daily_simulation_daemon`
2. `plan_submit_open_tasks_daemon`

职责边界固定为：

- 每天 mock 一批新用户，走完扫码、注册、建档、分配 clinician、填写答卷
- 持续扫描指定 plan 当前已进入 `opened` 的 task，并模拟用户完成答卷

plan 生命周期仍由 `worker` 内建 `plan_scheduler` 驱动。seeddata 不负责创建 task、推进 pending、过期 task，也不再承载历史修补脚本。

## 推荐入口

```bash
cd seeddata-runner
./scripts/run_seeddata_daemon.sh
```

## 直接运行

```bash
cd seeddata-runner
go run ./cmd/seeddata --config ./configs/seeddata.yaml
```

CLI 只保留：

- `--config`
- `--verbose`

行为约束：

- 不支持 `--steps`
- 不支持只运行单个 seed step
- `planSubmit.planIds` 必须在配置文件中提供
- `planSubmit.idleInterval` / `planSubmit.activeInterval` 控制 open-task submit daemon 的轮询间隔

## 配置

默认配置文件只保留 supervisor 需要的五段：

- `global`
- `api`
- `iam`
- `dailySimulation`
- `planSubmit`

参考文件：

- [configs/seeddata.yaml](./configs/seeddata.yaml)

如果不希望把 IAM 登录名和密码写进配置文件，可以直接使用环境变量覆盖：

- `IAM_USERNAME`
- `IAM_PASSWORD`

当 `api.token` 为空时，runner 会优先使用环境变量中的 IAM 凭据；未提供环境变量时，再回退到 `iam.username` / `iam.password`。

`dailySimulation` 当前支持两种调度语义：

- 旧模式：只配置 `runAt`，每天跑一次
- 新模式：配置 `windowStartAt`、`windowEndAt`、`interval`、`dailyMaxUsers`，在时间窗口内按固定间隔跑，并限制当日新增用户上限

`planSubmit` 当前支持的关键字段：

- `planIds`
- `workers`
- `idleInterval`
- `activeInterval`

默认轮询间隔：

- `idleInterval: 30s`
- `activeInterval: 5s`

## 代码结构

入口层只保留：

- [cmd/seeddata/main.go](./cmd/seeddata/main.go)
- [cmd/seeddata/seed_daily_simulation_daemon.go](./cmd/seeddata/seed_daily_simulation_daemon.go)
- [cmd/seeddata/seed_plan_submit_open_tasks_daemon.go](./cmd/seeddata/seed_plan_submit_open_tasks_daemon.go)

真正的业务实现下沉到模块内的 `internal/*`：

- `internal/seedconfig`
- `internal/seedruntime`
- `internal/dailysim`
- `internal/plansubmit`
- `internal/seedprofile`
- `internal/seedapi`
- `internal/seediauth`
- `internal/progress`
- `internal/chain`
- `internal/answersheet`

## 已移除

以下 step 与一次性工具已移除，不再支持：

- `staff`
- `clinician`
- `assign_testees`
- `testee_fixup_created_at`
- `actor_fixup_timestamps`
- `assessment_entries`
- `assessment_entry_flow`
- `assessment_by_entry`
- `journey_rebuild_history`
- `assessment`
- `plan`
- `plan_create_tasks`
- `plan_fixup_timestamps`
- `statistics_backfill`
- `assessment_retime_timestamps`
- `assessment_entry_fixup_timestamps`
- `cmd/tools/backfill-pending-assessments`
- `cmd/tools/redis-stats-ttl-fix`
