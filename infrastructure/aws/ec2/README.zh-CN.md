# AWS EC2 部署（免费套餐）

[English](./README.md) | **简体中文**

把 VoiceAgent 服务端部署到单台 AWS EC2 `t3.micro` 实例，用 Caddy 提供 HTTPS，并通过 GitHub Actions 实现自动 CI/CD。

## 你会得到什么

- **EC2 t3.micro** 实例（12 个月内符合免费套餐）
- **30 GB gp3** 根卷（符合免费套餐）
- **Elastic IP**，实例重启后地址不变
- **Caddy** 反向代理，自动 HTTPS（Let's Encrypt）
- **Docker Compose** 运行 Caddy 与 VoiceAgent 服务端（含内置 STUN/TURN）
- **GitHub Actions** 工作流：构建、推送到 GHCR，并通过 SSH 部署
- 安全组开放端口：`22`（SSH）、`80`/`443`（HTTP/HTTPS）、`3478` + `50000-60000`（WebRTC UDP）

## 前置条件

- [Terraform](https://developer.hashicorp.com/terraform/install) >= 1.5
- [AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) v2
- 一个 AWS 账号（符合免费套餐）
- 一个可管理 DNS 的域名（例如 `voice.streamcore.ai`）

## 第 0 步：AWS 账号与 CLI 配置

### 0a. 创建 AWS 账号

在 [aws.amazon.com](https://aws.amazon.com/free/) 注册。新账号有 12 个月免费套餐。需要绑定信用卡，但免费套餐额度内不会扣费。

### 0b. 为 Terraform 创建 IAM 用户

不要用 root 账号。创建一个专用 IAM 用户：

1. 进入 **IAM > Users > Create user**
2. 起名如 `terraform`
3. 附加 **AdministratorAccess** 策略（或收窄权限 —— 见下方说明）
4. 进入该用户的 **Security credentials** 标签页 > **Create access key**
5. 用途选择 **Command Line Interface (CLI)**
6. 保存 **Access Key ID** 和 **Secret Access Key** —— 下一步会用到

> **最小权限替代方案**：可以不用 `AdministratorAccess`，而是创建一个只含 `ec2:*`、`elasticipaddress:*` 和 `vpc:Describe*` 的自定义策略。上手阶段用 `AdministratorAccess` 更省事。

### 0c. 配置 AWS CLI

安装 CLI 后运行：

```bash
aws configure
```

它会提示输入四个值：

```
AWS Access Key ID:     <paste from step 0b>
AWS Secret Access Key: <paste from step 0b>
Default region name:   us-east-1
Default output format: json
```

这会把凭据写入 `~/.aws/credentials` —— Terraform 会自动读取。

### 0d. 验证是否可用

```bash
aws sts get-caller-identity
```

你应该能看到自己的账号 ID 和 IAM 用户 ARN。这一步能通，Terraform 也就能通。

## 第 1 步：创建 EC2 密钥对

如果你还没有：

```bash
aws ec2 create-key-pair \
  --key-name voiceagent \
  --query 'KeyMaterial' \
  --output text > voiceagent.pem

chmod 400 voiceagent.pem
```

## 第 2 步：用 Terraform 开通资源

```bash
cd server/infrastructure/aws/ec2

# Copy and edit the variables
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars — set key_pair_name to your key pair name

# Initialize and apply
terraform init
terraform plan
terraform apply
```

完成后 Terraform 会输出公网 IP 和 SSH 命令。

## 第 3 步：解析你的域名

创建一条 DNS **A 记录**，把你的域名指向 Terraform 输出的 Elastic IP：

```
voice.streamcore.ai  →  A  →  <PUBLIC_IP>
```

在你的 DNS 服务商（Cloudflare、Route 53、Namecheap 等）操作。Caddy 需要 DNS 先解析成功，才能签发 HTTPS 证书。

> **提示**：可用 `dig voice.streamcore.ai` 或 `nslookup voice.streamcore.ai` 验证是否已生效。

## 第 4 步：验证实例

SSH 登录实例（首次启动时 user-data 大约需要 2 分钟跑完）：

```bash
ssh -i voiceagent.pem ec2-user@<PUBLIC_IP>

# Check Docker is running
docker --version
```

## 第 5 步：在 `config.toml` 中配置 `public_ip`

WebRTC 需要服务端在 ICE 候选中公布自己的公网 IP。没有它，客户端无法与位于 NAT 之后的 EC2 实例建立媒体连接。

把 Elastic IP 加到生产 `config.toml` 的 `[server]` 段：

```toml
[server]
port = "8080"
public_ip = "<PUBLIC_IP>"   # Elastic IP from Terraform output — required for WebRTC on EC2
turn_secret = "changeme"    # Shared secret for the built-in STUN/TURN server
```

> **为什么**：EC2 实例只能看到自己的私网 IP（`172.31.x.x`）。没有 `public_ip` 时，WebRTC ICE 候选里带的是浏览器无法访问的私网地址。

## 第 6 步：配置 GitHub Actions Secrets

在你的 GitHub 仓库中，进入 **Settings > Secrets and variables > Actions**，添加以下 secret：

| Secret | 说明 |
|---|---|
| `EC2_HOST` | Terraform 输出的 Elastic IP |
| `EC2_SSH_KEY` | 你的 `.pem` 私钥文件内容 |
| `CONFIG_TOML` | 生产 `config.toml` 的完整内容（已设置 `public_ip`、`turn_secret` 和真实 API key） |
| `DOMAIN` | 你的域名（例如 `voice.streamcore.ai`） |

工作流使用自动提供的 `GITHUB_TOKEN` 访问 GHCR，因此无需额外 token。

## 第 7 步：部署

`.github/workflows/deploy-ec2.yml` 中的工作流会在推送到 `main` 时自动触发。你也可以在 Actions 标签页手动触发。

工作流做了什么：

1. **构建** —— 用 `server/Dockerfile` 构建 Docker 镜像
2. **推送** —— 推送到 GitHub Container Registry（`ghcr.io/<owner>/streamcore-server`）
3. **部署** —— SSH 登录 EC2，拉取新镜像，复制 compose 文件
4. **启动** —— 执行 `docker compose up -d`（Caddy + 带内置 STUN/TURN 的 VoiceAgent 服务端）
5. **健康检查** —— 请求 `http://localhost:8080/health` 确认服务已启动

Caddy 会在首次部署时自动签发 Let's Encrypt TLS 证书，并在到期前自动续期。

## 第 8 步：连接

部署完成且 DNS 生效后，你的 WHIP 端点是：

```
https://voice.streamcore.ai/whip
```

测试一下：

```bash
curl https://voice.streamcore.ai/health
# => ok
```

## 成本明细（免费套餐）

| 资源 | 免费额度 | 本方案用量 |
|---|---|---|
| EC2 t3.micro | 12 个月内每月 750 小时 | 1 台实例 |
| EBS gp3 | 12 个月内 30 GB | 30 GB |
| Elastic IP | 挂在运行中的实例上时免费 | 1 个 EIP |
| 数据传输 | 每月 100 GB 出站 | 视情况而定 |

> **注意**：Elastic IP 只在实例**运行中**时免费。如果你停掉实例，AWS 会按约 $0.005/小时收取未挂载 EIP 的费用。不用的时候要么终止实例，要么释放 EIP。

## 生产环境考量

这套免费套餐方案很适合开发和演示。用于生产时，请考虑：

- **实例规格** —— `t3.micro` 有 2 vCPU 和 1 GB 内存，可支撑约 4 路并发语音会话（会话限时 10 分钟）。约 6-8 路可考虑 `t3.small`，更重的负载考虑 `c6i.large`。
- **限制 SSH** —— 把 `ssh_allowed_cidrs` 设为你自己的 IP，而不是 `0.0.0.0/0`。
- **监控** —— 为 CPU、内存和磁盘启用 CloudWatch 告警。
- **备份** —— 如果磁盘上存了状态数据，配置 EBS 快照。

## 拆除

```bash
terraform destroy
```

这会移除 EC2 实例、安全组和 Elastic IP。

## 文件结构

```
infrastructure/aws/ec2/
  main.tf                  # EC2 instance, security group, EIP
  variables.tf             # Input variables
  outputs.tf               # Instance IP, SSH command, server URL
  terraform.tfvars.example # Example variable values
  Caddyfile                # Caddy reverse proxy config
  docker-compose.yml       # Runs Caddy + VoiceAgent server
  deploy-ec2.yml           # Reference copy of the GitHub Actions workflow
  .gitignore               # Ignore Terraform state and secrets
  README.md                # This file
```

真正的工作流文件位于服务端仓库根目录的 `.github/workflows/deploy-ec2.yml`。
