# ──────────────────────────────────────────────────────────────────────────────
# VoiceAgent Server — AWS EC2 Free Tier Deployment
# ──────────────────────────────────────────────────────────────────────────────
# This provisions a single t2.micro (free-tier eligible) instance running the
# VoiceAgent server via Docker. WebRTC requires UDP ports 3478 (built-in
# STUN/TURN) + 50000-60000 (ICE mux + TURN relay) in addition to HTTP on 8080.
# ──────────────────────────────────────────────────────────────────────────────

terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

# ── Data sources ─────────────────────────────────────────────────────────────

# Latest Amazon Linux 2023 AMI (free-tier eligible, arm64 not available on
# t2.micro so we use x86_64)
data "aws_ami" "amazon_linux" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

data "aws_vpc" "default" {
  default = true
}

# ── Security Group ───────────────────────────────────────────────────────────

resource "aws_security_group" "voiceagent" {
  name        = "voiceagent-server"
  description = "Allow SSH, HTTP/HTTPS (Caddy), and WebRTC traffic"
  vpc_id      = data.aws_vpc.default.id

  # SSH
  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.ssh_allowed_cidrs
  }

  # HTTP (Caddy redirect to HTTPS)
  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # HTTPS (Caddy reverse proxy)
  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # Built-in STUN/TURN server (UDP + TCP)
  ingress {
    description = "STUN/TURN UDP"
    from_port   = 3478
    to_port     = 3478
    protocol    = "udp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "STUN/TURN TCP"
    from_port   = 3478
    to_port     = 3478
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "WebRTC media ports"
    from_port   = 50000
    to_port     = 60000
    protocol    = "udp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "voiceagent-server"
  }
}

# ── EC2 Instance ─────────────────────────────────────────────────────────────

resource "aws_instance" "voiceagent" {
  ami                    = data.aws_ami.amazon_linux.id
  instance_type          = var.instance_type
  key_name               = var.key_pair_name
  vpc_security_group_ids = [aws_security_group.voiceagent.id]

  # Free tier: 30 GB gp3
  root_block_device {
    volume_size = 30
    volume_type = "gp3"
  }

  user_data = <<-EOF
    #!/bin/bash
    set -euo pipefail

    # Install Docker
    dnf update -y
    dnf install -y docker
    systemctl enable docker
    systemctl start docker
    usermod -aG docker ec2-user

    # Install Docker Compose plugin
    mkdir -p /usr/local/lib/docker/cli-plugins
    curl -SL "https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64" \
      -o /usr/local/lib/docker/cli-plugins/docker-compose
    chmod +x /usr/local/lib/docker/cli-plugins/docker-compose

    # Create app directory
    mkdir -p /opt/voiceagent
    chown ec2-user:ec2-user /opt/voiceagent
  EOF

  tags = {
    Name = "voiceagent-server"
  }
}

# ── Elastic IP (so the address survives stop/start) ─────────────────────────

resource "aws_eip" "voiceagent" {
  instance = aws_instance.voiceagent.id

  tags = {
    Name = "voiceagent-server"
  }
}
