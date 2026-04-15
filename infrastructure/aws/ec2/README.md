# AWS EC2 Deployment (Free Tier)

Deploy the VoiceAgent server to a single AWS EC2 `t3.micro` instance with HTTPS via Caddy and automatic CI/CD via GitHub Actions.

## What You Get

- **EC2 t3.micro** instance (free-tier eligible for 12 months)
- **30 GB gp3** root volume (free-tier eligible)
- **Elastic IP** so the address survives instance restarts
- **Caddy** reverse proxy with automatic HTTPS (Let's Encrypt)
- **Docker Compose** running Caddy and the VoiceAgent server (with built-in STUN/TURN)
- **GitHub Actions** workflow that builds, pushes to GHCR, and deploys via SSH
- Security group with ports: `22` (SSH), `80`/`443` (HTTP/HTTPS), `3478` + `50000-60000` (WebRTC UDP)

## Prerequisites

- [Terraform](https://developer.hashicorp.com/terraform/install) >= 1.5
- [AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) v2
- An AWS account (free-tier eligible)
- A domain name with DNS access (e.g., `voice.streamcore.ai`)

## Step 0: AWS Account & CLI Setup

### 0a. Create an AWS account

Sign up at [aws.amazon.com](https://aws.amazon.com/free/). New accounts get 12 months of free tier. You'll need a credit card on file but won't be charged for free-tier usage.

### 0b. Create an IAM user for Terraform

Don't use your root account. Create a dedicated IAM user:

1. Go to **IAM > Users > Create user**
2. Name it something like `terraform`
3. Attach the **AdministratorAccess** policy (or scope it down — see note below)
4. Go to the user's **Security credentials** tab > **Create access key**
5. Choose **Command Line Interface (CLI)** as the use case
6. Save the **Access Key ID** and **Secret Access Key** — you'll need them next

> **Least-privilege alternative**: Instead of `AdministratorAccess`, you can create a custom policy with just `ec2:*`, `elasticipaddress:*`, and `vpc:Describe*`. `AdministratorAccess` is simpler for getting started.

### 0c. Configure the AWS CLI

Install the CLI, then run:

```bash
aws configure
```

It will prompt for four values:

```
AWS Access Key ID:     <paste from step 0b>
AWS Secret Access Key: <paste from step 0b>
Default region name:   us-east-1
Default output format: json
```

This writes credentials to `~/.aws/credentials` — Terraform reads them automatically.

### 0d. Verify it works

```bash
aws sts get-caller-identity
```

You should see your account ID and IAM user ARN. If this works, Terraform will too.

## Step 1: Create an EC2 Key Pair

If you don't already have one:

```bash
aws ec2 create-key-pair \
  --key-name voiceagent \
  --query 'KeyMaterial' \
  --output text > voiceagent.pem

chmod 400 voiceagent.pem
```

## Step 2: Provision with Terraform

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

Terraform will output the public IP and SSH command when done.

## Step 3: Point Your Domain

Create a DNS **A record** pointing your domain to the Elastic IP from the Terraform output:

```
voice.streamcore.ai  →  A  →  <PUBLIC_IP>
```

Do this in your DNS provider (Cloudflare, Route 53, Namecheap, etc.). Caddy needs DNS to resolve before it can provision the HTTPS certificate.

> **Tip**: You can verify propagation with `dig voice.streamcore.ai` or `nslookup voice.streamcore.ai`.

## Step 4: Verify the Instance

SSH into the instance (it takes ~2 minutes for user-data to finish on first boot):

```bash
ssh -i voiceagent.pem ec2-user@<PUBLIC_IP>

# Check Docker is running
docker --version
```

## Step 5: Configure `public_ip` in `config.toml`

WebRTC requires the server to advertise its public IP for ICE candidates. Without this, clients can't establish media connections to an EC2 instance behind NAT.

Add the Elastic IP to the `[server]` section of your production `config.toml`:

```toml
[server]
port = "8080"
public_ip = "<PUBLIC_IP>"   # Elastic IP from Terraform output — required for WebRTC on EC2
turn_secret = "changeme"    # Shared secret for the built-in STUN/TURN server
```

> **Why**: EC2 instances only see their private IP (`172.31.x.x`). Without `public_ip`, WebRTC ICE candidates contain the private IP which browsers can't reach.

## Step 6: Set Up GitHub Actions Secrets

In your GitHub repository, go to **Settings > Secrets and variables > Actions** and add these secrets:

| Secret | Description |
|---|---|
| `EC2_HOST` | The Elastic IP from Terraform output |
| `EC2_SSH_KEY` | Contents of your `.pem` private key file |
| `CONFIG_TOML` | Full contents of your production `config.toml` (with `public_ip`, `turn_secret` set, and real API keys) |
| `DOMAIN` | Your domain name (e.g., `voice.streamcore.ai`) |

The workflow uses `GITHUB_TOKEN` (automatic) for GHCR, so no extra token is needed.

## Step 7: Deploy

The workflow at `.github/workflows/deploy-ec2.yml` triggers automatically on push to `main`. You can also trigger it manually from the Actions tab.

What the workflow does:

1. **Build** — builds the Docker image from `server/Dockerfile`
2. **Push** — pushes to GitHub Container Registry (`ghcr.io/<owner>/voiceagent-server`)
3. **Deploy** — SSHs into EC2, pulls the new image, copies compose files
4. **Start** — runs `docker compose up -d` (Caddy + VoiceAgent server with built-in STUN/TURN)
5. **Health check** — hits `http://localhost:8080/health` to verify the server started

Caddy automatically provisions a Let's Encrypt TLS certificate on first deploy and renews it before expiry.

## Step 8: Connect

Once deployed and DNS has propagated, your WHIP endpoint is:

```
https://voice.streamcore.ai/whip
```

Test it:

```bash
curl https://voice.streamcore.ai/health
# => ok
```

## Cost Breakdown (Free Tier)

| Resource | Free Tier Allowance | This Setup |
|---|---|---|
| EC2 t3.micro | 750 hrs/month for 12 months | 1 instance |
| EBS gp3 | 30 GB for 12 months | 30 GB |
| Elastic IP | Free while attached to a running instance | 1 EIP |
| Data transfer | 100 GB/month outbound | Varies |

> **Note**: The Elastic IP is free only while the instance is **running**. If you stop the instance, AWS charges ~$0.005/hr for the unattached EIP. Either terminate the instance or release the EIP when not in use.

## Production Considerations

This free-tier setup is great for development and demos. For production, consider:

- **Instance size** — `t3.micro` has 2 vCPUs and 1 GB RAM. Supports ~4 concurrent voice sessions (with 10-minute session limits). Consider `t3.small` for ~6-8 sessions or `c6i.large` for heavier workloads.
- **Restrict SSH** — set `ssh_allowed_cidrs` to your IP instead of `0.0.0.0/0`.
- **Monitoring** — enable CloudWatch alarms for CPU, memory, and disk.
- **Backups** — set up EBS snapshots if you store any state on disk.

## Tear Down

```bash
terraform destroy
```

This removes the EC2 instance, security group, and Elastic IP.

## File Structure

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

The actual workflow lives at `.github/workflows/deploy-ec2.yml` in the server repo root.
