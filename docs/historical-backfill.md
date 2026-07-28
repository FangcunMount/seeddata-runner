# 一次性历史回填运行手册

历史命令复用 daemon 的 Journey 状态机，但每天只生成一次确定性批次。普通 daemon
不携带历史上下文，行为和原有账本身份保持不变。

## 前置条件

- IAM `mockConsumer.enabled=true`，`maxConcurrent: 1`；共享密钥只通过
  `IAM_MOCK_CONSUMER_SHARED_SECRET` 注入。
- qs-server apiserver 与 collection-server 使用相同的
  `QS_HISTORICAL_CONTEXT_SECRET`，并已启用限定 Org、`2025-01-01..2026-07-27` 的历史开关。
- apiserver 设置 `historical_seed.pause_plan_scheduler: true`；回填期间停用独立 Plan scheduler。
- Daily Simulation 配置 `countMin: 40`、`countMax: 200`、`workers: 5`，Journey 权重为
  `10/15/25/50`，Plan 列表、入口和已发布 target 版本均已确认。
- 已保存 Statistics 回填前基线；worker 正常运行，且没有同时运行普通 seeddata daemon。

## 构建与预检

```bash
go build -o tmp/bin/seeddata ./cmd/seeddata
go test ./...
export IAM_MOCK_CONSUMER_SHARED_SECRET='<secret>'
export QS_HISTORICAL_CONTEXT_SECRET='<secret>'
```

先用 3 天范围执行本地或 staging 验证；成功后使用正式批次 ID：

```bash
tmp/bin/seeddata historical-backfill \
  --config configs/seeddata.yaml \
  --from 2025-01-01 \
  --to 2026-07-27 \
  --batch-id hist-20250101-20260727-v1
```

命令仅在当天所有场景达到终态后写 checkpoint。任何终态失败会停止在当天，修复原因后使用
同一批次、相同配置和相同版本恢复：

```bash
tmp/bin/seeddata historical-backfill \
  --config configs/seeddata.yaml \
  --from 2025-01-01 \
  --to 2026-07-27 \
  --batch-id hist-20250101-20260727-v1 \
  --resume
```

不得更换 batch ID 或幂等键来绕过 payload conflict。出现 target/Plan 版本漂移时，恢复原冻结版本或新建经审批的批次，不能继续原批次。

## 只读检查

```bash
tmp/bin/seeddata historical-verify \
  --config configs/seeddata.yaml \
  --batch-id hist-20250101-20260727-v1

tmp/bin/seeddata historical-manifest \
  --batch-id hist-20250101-20260727-v1
```

`historical-verify` 同时检查本地 checkpoint/manifest 和 qs-server stage ledger；需要
Assessment 的场景必须存在 Assessment、Outcome、Report，Plan 子场景必须存在 task open/complete。

## 后续和失败处理

runner 完成后，按 qs-server 运行手册执行 19 个 Statistics repair/validate 窗口、最终 publish
和精确 Statistics 对账。失败时保留 `.seeddata-cache/historical`、服务端 stage ledger 和日志。
需要回滚时，先停 runner、worker、Plan scheduler，使用 manifest 和 qs-server 批次回滚工具；不得按 Org
或模糊时间范围清理。
