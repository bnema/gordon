package cli

type App struct {
	Config Config
}

type Config struct {
	General GeneralConfig `yaml:"General"`
	Http    HttpConfig    `yaml:"Http"`
}

type GeneralConfig struct {
	Token string `yaml:"token"`
}

type HttpConfig struct {
	BackendURL string `yaml:"backendURL"`
}
