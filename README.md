# WorkBuddy2API Stack

把 CodeBuddy 账号包装成 OpenAI 兼容 API 的自托管网关，外加一个可视化管理面板与若干部署辅助脚本。

本仓库由三部分组成：

| 目录 | 内容 | 说明 |
| --- | --- | --- |
| `gateway/` | Go 网关 | OAuth 设备授权登录、账号池调度、熔断/冷却、会话粘性、SSE 流式转发 |
| `gui/` | Go + React 管理面板 | 账号池可视化、在线改配置、批量签到、聊天测试台 |
| `deploy/` | 部署与验证脚本 | 接口自检、QA 脚本、受限 API 对外分享（nginx 反代 + Tailscale Funnel） |

## 快速开始

### 1. 网关

```bash
cd gateway
cp config.example.json config.json
# 编辑 config.json，至少设置 api_key（留空 = 不鉴权，公网部署务必设置）
docker compose up -d --build
```

首次启动后登录账号（OAuth 设备授权）：

```bash
docker compose exec -it wb2api bash -c './login.sh'
docker compose restart wb2api
```

默认监听 `http://127.0.0.1:7863`，健康检查 `GET /healthz`。

### 2. 管理面板

```bash
cd gui
docker compose up -d --build
```

访问 `http://<服务器IP>:8787`，默认账号 `admin` / `workbuddy`（**请立即修改**）。
`docker-compose.yml` 默认把账号目录与网关配置挂载为 `../gateway/`，如果网关部署在别处请改成实际路径。

### 3. 验证

```powershell
# 网关基本连通性 + 流式 + 非流式 + 非法 key
./deploy/Test-Api.ps1

# Responses 协议（工具调用回放、错误码）
BASE_URL=http://127.0.0.1:7863 API_KEY=<你的 api_key> node deploy/Test-Harness.mjs
```

## 目录结构

```
.
├── .github/workflows/build.yml   # 定时多架构构建并推送 ghcr
├── gateway/                      # 网关源码、Dockerfile、compose
├── gui/                          # 管理面板源码（React + Go）、Dockerfile、compose
├── deploy/                       # 部署脚本与验证脚本（见 deploy/README.md）
├── NOTICE                        # 上游版权与修改说明
└── README.md
```

## 安全须知

- `config.json`、`auths/`、`data/` 保存真实密钥与账号凭证，**绝不要提交到版本库**（已在 `.gitignore` 中排除）。
- 面板的 `docker-compose.yml` 里预置口令仅供首次登录，部署后请立刻改掉，并放在 HTTPS 反代之后。
- 端口 `7863`（网关）、`8787`（面板）默认都建议只在内网/本机访问；对外分享请走 `deploy/sharing` 里的受限反代。

## 许可

本项目基于 [workbuddy2api](https://github.com/Sliverkiss/workbuddy2api)（MIT License, Copyright (c) 2026 Sliverkiss）二次开发，
并在其基础上整合了管理面板与部署脚本。

```
Copyright (c) 2026 Sliverkiss        （上游原始代码）
Copyright (c) 2026 luo               （本衍生版本的修改部分）
```

再分发时请保留原始版权与许可声明，详见 [LICENSE](LICENSE)、[NOTICE](NOTICE) 与各子目录下的 `LICENSE`。

本项目仅供个人学习与研究使用，请仅使用本人已授权的账号，并遵守上游平台的服务条款。
