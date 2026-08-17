# Base62 编解码与短码注册服务 (Base62 Codec & Short-Code Registry)

## 业务问题

本项目实现一个非负整数与 Base62 字符串的双向编解码服务，并在其上构建一个短码注册表：来源标识经单调递增计数器分配短码（Base62 编码、幂等），支持预占指定短码（碰撞检测）、解析短码回来源、批量分配（批内去重）以及统计与碰撞计数。所有状态保存在内存中，通过 HTTP+JSON 暴露能力。

核心特性：
- **规范编码**：字符集 `0-9A-Za-z`（0..61），大端序，零编码为 `"0"`，多字符串无前导零，大小写敏感（`A`=10，`a`=36）。
- **解码溢出保护**：区分格式错误（`ErrFormat`）与溢出错误（`ErrOverflow`）；长度 >11 必溢出，长度 =11 按数值判定，`math/bits` 检测乘加溢出。
- **幂等分配**：`Alloc` 对重复来源返回原码、不推进计数器；`AllocBatch` 批内去重，重复来源复用同一码且只消耗一次计数器。
- **碰撞不变式**：分配与预占均校验反向映射；计数器产出的码若已被预占给其它来源，则计一次碰撞、不推进计数器、不覆盖既有绑定。
- **双射维护**：来源→码、码→来源始终保持双射；预占新码时释放来源持有的旧码。

主要输入/输出：

| 接口 | 输入 | 输出 |
| --- | --- | --- |
| `POST /encode` | `{"n"}` | `{"ok":true,"code"}` |
| `POST /decode` | `{"code"}` | `{"ok":true,"n"}` 或 `{"ok":false,"error":"格式错误"|"溢出错误"}` |
| `POST /alloc` | `{"source"}` | `{"ok":true,"code","created"}` |
| `POST /reserve` | `{"source","code"}` | `{"ok":true,"code","created"}` 或碰撞 `{"ok":false,"error":"碰撞","conflict_source"}` |
| `GET /resolve?code=` | — | `{"ok":true,"code","source"}` 或 `{"ok":false,"error":"格式错误"|"未找到"}` |
| `POST /alloc-batch` | `{"sources":[...]}` | `{"ok":true,"results":[{"source","code","created"}]}` |
| `GET /stats` | — | `{"ok":true,"sources","codes","next_counter","collisions"}` |
| `GET /healthz` | — | `200 {"ok":true}` |

## 本地命令

```bash
go build ./...          # 编译
go run . --smoke-test   # 自检（不监听端口，成功退出码 0）
go test ./...           # 单元测试（base62 编解码 + registry 注册表）
```

启动 HTTP 服务：`go run .`（默认监听 `:8080`，可用 `--addr :9090` 指定）。

## Docker

构建脚本 `build_benzhi_docker.sh` 接受两个参数：

1. `IMAGE_NAME`：镜像名（默认 `my-project`）。
2. `DOCKER_PLATFORM`：目标平台（默认 `linux/amd64`）。

构建 amd64 与 arm64 评测镜像：

```bash
bash ./build_benzhi_docker.sh go-task-benzhi:amd64 linux/amd64
bash ./build_benzhi_docker.sh go-task-benzhi:arm64 linux/arm64
```

进入容器交互式 shell：

```bash
docker run -it go-task-benzhi:amd64
```

双架构运行时镜像（交付用 `Dockerfile`）：

```bash
docker buildx build --platform linux/amd64 --load -t go-task-check:amd64 .
docker run --rm go-task-check:amd64 --smoke-test
docker buildx build --platform linux/arm64 --load -t go-task-check:arm64 .
docker run --rm go-task-check:arm64 --smoke-test
```

## 技术栈

- Go `1.26.3`（`/opt/homebrew/bin/go`），`GOTOOLCHAIN=local`，仅标准库（math/bits、encoding/json、net/http、sync）。
- 交付镜像 `CGO_ENABLED=0`，`linux/amd64` 与 `linux/arm64` 双架构。
