# Model Proxy

大模型 API 代理系统，透明转发请求并记录完整日志，用于理解 LLM 工具通信过程和构建训练数据集。

## 快速开始

```bash
# 构建（需 Go 1.22+）
go build -o model-proxy .

# 运行
./model-proxy
```

访问 `http://localhost:8089`。

**零依赖启动**——数据库内嵌 SQLite，数据文件自动创建在 `data/proxy.db`，无需安装 MySQL。

## 使用方式

1. 打开 `http://localhost:8089`，进入「代理配置」页签
2. 新增一条配置：填名称和目标主机（如 `api.deepseek.com`），标识自动生成
3. 表格中会显示**代理 URL**（如 `http://localhost:8089/api/abc123`），点复制
4. 将 LLM 工具的 API Base URL 改为这个代理 URL

所有请求会被转发到目标主机，日志实时记录在「请求日志」页签中。

## API

| 端点 | 用途 |
|------|------|
| `/api/{code}/**` | 代理转发，根据 `{code}` 查找目标主机转发 |
| `/api/config/list` | 代理配置列表 |
| `/api/config/add` | 新增配置 |
| `/api/config/update` | 更新配置 |
| `/api/config/delete/{id}` | 删除配置 |
| `/api/log/list` | 请求日志列表（支持 `?limit=&offset=&keyword=&status=`） |
| `/api/log/payload/{id}` | 单条请求完整数据（按需懒加载） |
| `/api/log/delete/{id}` | 删除日志 |
| `/api/logs/stream` | SSE 实时日志推送 |
| `/api/stats` | 统计（总量、成功率、平均耗时、TTFT） |

## 项目结构

```
model-proxy-go/
├── main.go                          # 入口，路由注册
├── internal/
│   ├── db/sqlite.go                 # SQLite 初始化 + CRUD
│   ├── proxy/
│   │   ├── handler.go               # 反向代理 + 结构化日志
│   │   └── sse.go                   # SSE 推送中枢
│   ├── api/handlers.go              # REST API 处理器
│   └── models/types.go              # 数据结构定义
├── static/index.html                # 单页前端（日志 + 配置）
├── data/proxy.db                    # SQLite 数据文件（运行时生成）
└── model-proxy                      # 编译产物
```

## 数据存储

两张表，SQLite WAL 模式：

**proxy_config** — 代理配置：`id, proxy_code, proxy_name, target_url`

**requests** — 请求记录：
- `summary_json`：轻量元数据（状态、耗时、TTFT、字符数），列表查询走这里
- `payload_json`：完整数据（请求头、请求体、响应体、日志事件流），详情页按需加载

日志事件按阶段标记：`receive → proxy → stream → complete → error`，每条带时间戳、级别、来源和详情。

## 与 Java 版对比

| | Java 版 | Go 版 |
|---|---|---|
| 启动时间 | ~3s（JVM） | 毫秒级 |
| 内存占用 | 200MB+ | ~30MB |
| 数据库 | MySQL（需额外安装） | SQLite（嵌入式） |
| 日志结构 | 单条 LONGTEXT | summary + payload 双表 |
| 实时推送 | 无 | SSE |
| 部署 | JAR + JRE + MySQL | 一个 15MB 文件 |

## 配置

编辑 `main.go` 修改端口（默认 8089），重新编译即可。无需外部配置文件。
