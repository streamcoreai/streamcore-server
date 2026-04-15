output "instance_id" {
  description = "EC2 instance ID"
  value       = aws_instance.voiceagent.id
}

output "public_ip" {
  description = "Elastic IP address of the server"
  value       = aws_eip.voiceagent.public_ip
}

output "ssh_command" {
  description = "SSH command to connect to the instance"
  value       = "ssh -i <your-key>.pem ec2-user@${aws_eip.voiceagent.public_ip}"
}

output "server_url" {
  description = "VoiceAgent server WHIP endpoint (point your domain's DNS A record to the public_ip first)"
  value       = "https://voice.streamcore.ai/whip"
}

output "dns_record" {
  description = "Create this DNS A record for your domain"
  value       = "A record: voice.streamcore.ai -> ${aws_eip.voiceagent.public_ip}"
}
