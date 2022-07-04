
group "default" {
  targets = ["web"]
}

target "web" {
  context = "./"
  dockerfile = "Dockerfile"
  output = ["type=registry"]
  driver = "docker-container"
  tags = ["docker.io/paullj1/mypersonalbudget:1.3.0",
          "docker.io/paullj1/mypersonalbudget:latest"]
  platforms = ["linux/amd64", "linux/arm64", "linux/arm/v7"]
}

