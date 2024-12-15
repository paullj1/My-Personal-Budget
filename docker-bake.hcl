
group "default" {
  targets = ["web"]
}

target "web" {
  context = "./"
  dockerfile = "Dockerfile"
  output = ["type=registry"]
  tags = ["docker.io/paullj1/mypersonalbudget:2024.12.15.0",
          "docker.io/paullj1/mypersonalbudget:latest"]
  platforms = ["linux/arm64"]
}

