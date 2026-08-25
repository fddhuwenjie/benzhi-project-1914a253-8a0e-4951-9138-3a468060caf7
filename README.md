# 展柜微环境异常处置台

面向博物馆预防性保护团队的微环境异常闭环服务，接收展柜温湿度异常，完成风险研判、现场检查、临时调控、专家复核并可追溯关闭。

## 构建、运行与测试

```bash
go test ./...
go run ./cmd/microclimate -addr=127.0.0.1:19081
go run ./cmd/microclimate -self-check -addr=127.0.0.1:19081
```

服务默认监听 `127.0.0.1:19081`，也可通过 `-addr` 或 `PORT` 环境变量配置。数据快照默认写入当前目录 `.microclimate-snapshot.json`，可用 `-snapshot` 指定路径。

事件接口会校验 RFC3339 采集时间、传感器单位和持续时长，并支持 `idempotency_key` 安全重试；可选 `rule_version` 会把阈值依据快照写入处置单。同一展柜二十四小时内的开放案件会建立复发关联，关闭案件不会再次关联。`GET /v1/cases` 支持 `cabinet_id`、`status`、`risk_level`、`assignee_id`、`updated_before`、`overdue`、`limit`、`cursor` 组合过滤分页，并返回责任负载、逾期计数和快照恢复诊断。现场复测校验时间顺序、持续时长和读数跳变，临时调控动作会记录效果时间线；专家复核隔离现场检查人员并支持幂等重放。处置单关闭后可通过 `GET /v1/cases/{id}/trace-summary` 独立校验 SHA-256 追溯摘要。

检查接口支持 `recalculate` 与 `rule_version` 规则重算、`checklist_receipts` 逐项回执和整改任务确认；高、紧急风险必须完成连续稳定复测窗口后才能复核。案件列表可通过 `from`、`to` 查询复发次数、风险分布和平均关闭耗时，响应同时包含恢复一致性报告。

异常上报也接受 1～100 条 JSON 数组并逐项返回 `created`、`replay` 或校验错误。`assign`、`inspections`、`reviews`、`close` 可通过 `X-Operator` 与 `X-Role` 记录授权审计；检查支持 `dry_run` 规则差异预览及带 token 的确认。复测会识别风险方向振荡并保留告警、整改期限、传感器质量标记和紧急案件双专家会签状态。已关闭案件可用 `trace-summary?format=json|package&redact=true` 下载脱敏追溯包并校验哈希。

接口按职责校验角色：分派和关闭使用 `保护专员`，现场检查使用 `值班保管员`，首位复核使用 `文保专家`，紧急案件会签使用不同的 `文保技术专家`。批量异常逐项返回 `created`、`replay`、`conflict` 或 `validation_failed`，字段错误不会影响同批次其他事件。

传感器质量不足的事件会标记为待复核，可通过 `POST /v1/cases/{id}/quality-review` 由保护专员确认或驳回；分派后保管员可调用 `POST /v1/cases/{id}/accept` 确认接收或拒绝。证据损坏可使用 `POST /v1/cases/{id}/evidence/replace` 提交新旧摘要、原因和操作者，案件详情保留替换链。复核退回再审时携带 `previous_decision_id`，整改任务支持 `depends_on` 依赖。`trace-summary` 支持 `verify_chain`、角色/类型/时间过滤及游标分页。
