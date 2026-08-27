# 字幕发布工作台

`caption-release-workbench` 面向剧场无障碍字幕制作团队，用于把一份演出字幕包从建档、WebVTT 导入、确定性质量检查、问题整改和独立复核推进到冻结、凭据签发、离线验证与最终发布。

工作台由 Go 服务直接提供响应式 HTML、CSS、JavaScript 和同源 JSON API，不需要 Node 构建链。业务数据保存在本地 SQLite；冻结清单使用 SHA-256 校验，发布凭据使用本地 Ed25519 密钥签名。每个状态变更都要求 `expectedVersion` 和 `idempotencyKey`，并在短事务中固化业务投影、幂等结果与不可变审计事件。

## 构建

要求 Go 1.23 或更高版本。在项目根目录执行：

```text
go build ./...
```

## 运行

默认仅监听高位回环地址 `127.0.0.1:19087`：

```text
go run ./cmd/server
```

也可以显式指定地址和数据文件：

```text
go run ./cmd/server -addr=127.0.0.1:19187 -db=caption-workbench.db -key=caption-workbench.key
```

未提供 `-addr` 时，可通过 `PORT` 传入端口号，服务会绑定 `127.0.0.1:<PORT>`。服务拒绝 `0.0.0.0`、`[::]` 和缺失主机的通配监听。首次启动会自动执行可重复的 SQLite 迁移并生成权限为 `0600` 的 Ed25519 私钥文件。浏览器访问监听地址即可使用工作台。

## 测试与自检

运行全部单元和集成测试：

```text
go test ./...
```

运行有界端到端自检：

```text
go run ./cmd/server -addr=127.0.0.1:19087 -selfcheck
```

自检会建立临时 SQLite 数据库，在真实回环 HTTP 服务上通过与浏览器相同的 JSON API 完成建档、时间轴导入、送审、独立复核、清单冻结、凭据签发、离线验证和发布确认，然后主动关闭服务并清理临时文件。

## 主要业务约束

- 状态依次为草稿、待整改、待复核、已复核、已冻结和已发布；冻结后拒绝内容修改。
- 制作员不能审批自己的提交，发布负责人必须独立于制作员和校审员。
- 校验器检查乱序、重叠、越界、间隔过短、阅读速度、行长、缺失说话人与音效描述格式。
- 退回后必须产生新字幕修订才能再次送审；开放阻断项会阻止送审，任何开放问题会阻止冻结。
- 发布凭据采用 `CRW1` 格式，可在不访问外部服务的情况下验证签名，并与当前项目、冻结摘要和冻结版本核对。

## 工作台查询与发布包

- `GET /api/projects` 支持 `status`、`language`、`producerId`、`reviewerId`、`limit` 和 `offset`，列表项同时返回最新修订号、开放问题数和开放阻断项数。
- `GET /api/projects/{projectId}/revisions` 返回经过摘要校验的修订时间线；项目详情可用 `revisionNo` 读取历史版本，`revisionNo` 搭配 `compareTo` 查看相邻版本字段差异。
- `POST /api/projects/{projectId}/findings/batch-resolve` 在一个幂等事务中处置同一最新修订的多个问题；`GET /api/projects/{projectId}/quality-report` 提供确定性质量与可读性统计。
- 项目冻结后可通过 `GET /api/projects/{projectId}/export` 下载确定性 ZIP 发布包，包内包含 `manifest.json`、`normalized.vtt` 和 `metadata.json`，响应头提供包摘要。

## 数据与安全

默认数据文件是 `caption-workbench.db`，默认密钥文件是 `caption-workbench.key`。请将二者作为同一发布环境的受控资产备份，尤其不要将私钥提交到版本控制。HTTP 响应设置基础安全头，JSON 请求限制大小并拒绝未知字段；生产部署时仍应由受信任的本机访问控制或反向代理补充身份认证和 TLS。
