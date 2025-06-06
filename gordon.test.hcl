server {
  port = 80
  registry_port = 5000
  runtime = "docker"
  ssl_email = "admin@example.com"
  data_dir = "./data"
}

registry_auth {
  enabled = true
  username = "gordon"
  password = "secret123"
}

routes = {
  "test.local.tld" = "ghcr.io/bnema/go-hello-world-http:latest"
}