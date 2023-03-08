
group "default" {
  targets = ["web"]
}

target "web" {
  context = "./"
  dockerfile = "Dockerfile"
  output = ["type=registry"]
  tags = ["docker.io/paullj1/mypersonalbudget:1.4.5",
          "docker.io/paullj1/mypersonalbudget:latest"]
  platforms = ["linux/arm64", "linux/arm/v7"]
}

