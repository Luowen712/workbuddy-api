# deploy

部署辅助与验证脚本。全部脚本默认按本仓库的多目录布局取值，可用参数或环境变量覆盖。

## 文件

| 文件 | 用途 |
| --- | --- |
| `Test-Api.ps1` | 网关连通性自检：`/v1/models`、chat 流式与非流式、非法 key 必须 401 |
| `Test-Harness.mjs` | OpenAI Responses 协议一致性：非流式、function call 回放、错误码；可选接真实客户端库 |
| `Test-Access.ps1` | 面板 + 网关的 key 轮换 / 重启生效 QA（自建 fixture，用后自动清理容器） |
| `sharing/` | 受限 API 对外分享：nginx 端点白名单 + 独立 key + 限流，可选 Tailscale Funnel |

## 用法

### Test-Api.ps1

```powershell
# 默认读取 ../gateway/config.json，请求 http://localhost:7863
./Test-Api.ps1

# 自定义
./Test-Api.ps1 -ConfigPath /path/to/config.json -Base http://127.0.0.1:7863
```

脚本需要目标配置里已设置 `api_key`，并期望模型列表包含 `cn:fast-model`。

### Test-Harness.mjs

```bash
BASE_URL=http://127.0.0.1:7863 API_KEY=<key> MODEL=cn:fast-model node Test-Harness.mjs
```

可选 `PI_AI_PATH` 指向 `@earendil-works/pi-ai` 的 `openai-responses.js`，额外验证真实客户端库的流式链路。

### Test-Access.ps1

```powershell
./Test-Access.ps1 -GuiImage workbuddy2api-gui:latest
```

会在临时目录生成 QA 配置、启动两个临时容器（端口 17863 / 18787），验证面板改 key 后网关重启生效、旧 key 失效，结束后清理。

## sharing：受限 API 外分享

把本机网关的少量只读接口（`GET /v1/models`、`POST /v1/chat/completions`、`POST /v1/responses`）
通过独立 key 分享给可信的人，其余路径一律 404，并带速率与并发限制。

```powershell
# 1. 生成共享 key 并渲染 nginx 配置（写入 ./private/，不要提交）
./sharing/Configure-Sharing.ps1 -PublicBaseUrl https://your-domain.example.com

# 2. 构建 Tailscale 镜像（需要先把 tailscale 静态二进制放到 ./sharing/tailscale_1.102.4_amd64/）
#    不用 Funnel 时可以删掉 compose 里的 tunnel 服务
docker build -f sharing/Tailscale.Dockerfile -t workbuddy-tailscale:1.102.4 sharing

# 3. 启动
docker compose -f sharing/compose.yml up -d

# 4. 本地验证
node sharing/Test-Sharing.mjs
```

要点：

- 共享 key 与网关原 key 相互独立，网关原 key 在分享入口会被拒绝；
- 只有 `127.0.0.1:17864` 发布到宿主机，Tailscale Funnel 也只能指向反代的 8080；
- `sharing/private/` 与 `sharing/tailscale/` 含密钥与设备状态，已在 `.gitignore` 中排除；
- 出口如需经本地 HTTP 代理，修改 `sharing/compose.yml` 里的 `HTTP_PROXY`/`HTTPS_PROXY`。
