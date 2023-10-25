package common

type Config struct {
	General         GeneralConfig         `yaml:"General"`
	Http            HttpConfig            `yaml:"Http"`
	Admin           AdminConfig           `yaml:"Admin"`
	ContainerEngine ContainerEngineConfig `yaml:"ContainerEngine"`
}

type AdminConfig struct {
	Path string `yaml:"path"`
}

type GeneralConfig struct {
	RunEnv       string // come from env
	BuildDir     string // come from env
	BuildVersion string // come from env
	StorageDir   string `yaml:"storageDir"` // = buildir/storage
	Token        string `yaml:"token"`
}
type HttpConfig struct {
	Port       int    `yaml:"port"`
	TopDomain  string `yaml:"topDomain"`
	SubDomain  string `yaml:"subDomain"`
	BackendURL string `yaml:"backendURL"`
}

type ContainerEngineConfig struct {
	Sock       string `yaml:"dockersock"`
	PodmanSock string `yaml:"podmansock"`
	Podman     bool   `yaml:"podman"`
	Network    string `yaml:"network"`
}

// config.Http.BackendURL = readUserInput("Enter the backend URL (e.g. https://gordon.mydomain.com):")
// config.General.Token = readUserInput("Enter the token (check your backend config.yml):")
