# Seeddata Guide

## 模块定位

`seeddata-runner` 只负责当前日期的普通每日模拟：

1. `daily_simulation_daemon` 完成 Guardian、Testee、Entry、Plan enrollment、AnswerSheet、Assessment 和 Report 闭环。
2. `plan_submit_open_tasks_daemon` 查询当天 opened task，并做幂等 admin submit。

## 运行

```bash
./scripts/run_seeddata_daemon.sh
```

或：

```bash
go run ./cmd/seeddata --config ./configs/seeddata.yaml
```

CLI 只接受 `--config` 和 `--verbose`；配置只接受 README 中列出的严格字段。

## daily simulation

完整旅程顺序：

1. ensure Guardian account
2. create or reuse current-date Testee
3. resolve and intake Assessment Entry
4. enroll Plan and record returned task IDs
5. submit AnswerSheet
6. wait assessment readiness
7. wait report interpreted

需要测评的目标必须完成第 7 步才算本轮成功。普通 Questionnaire 不需要 readiness/report。

after-hours catch-up 只匹配当前日期、相同 source/tags/身份签名的 Testee。

## plan submit

plan submit daemon：

- 只查当前日期任务窗口。
- 只处理 configured Plan、opened task 和 seeddata Testee。
- 使用独立持久账本保护幂等和冲突语义。
- 不负责创建、打开、过期或重排任务。

## 凭据

生产环境通过环境变量注入：

- `IAM_USERNAME`
- `IAM_PASSWORD`
- `IAM_MOCK_CONSUMER_SHARED_SECRET`

不要把真实 secret 写入 YAML、日志或提交记录。

## 故障判断

- `accepted_pending`：AnswerSheet 已持久接受，Assessment 尚未 ready；重试只查 readiness。
- `ready` 且本轮报错：Assessment 已持久化，Report 尚未 interpreted 或已 failed；重试只查 report。
- `conflict`：同一 logical ID 的 payload fingerprint 已变化，需要人工检查配置或数据身份。
- daemon 状态文件控制当前日期 slot 和配额；submission 状态文件控制远端副作用幂等，两者不能互相替代。

详细配置、构建、systemd 和验收步骤见 [README.md](./README.md)。
