server {
  port = 8080
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
  "example.com" = "nginx:latest"
  "api.example.com" = "myapi:latest"
  "http://dev.example.com" = "myblog:latest"
}