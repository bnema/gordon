server {
  port = 8080
  registry_port = 5000
  runtime = "docker"
  ssl_email = "admin@example.com"
  data_dir = "./data"
}

routes = {
  "example.com" = "nginx:latest"
  "api.example.com" = "myapi:latest"
  "http://dev.example.com" = "myblog:latest"
}