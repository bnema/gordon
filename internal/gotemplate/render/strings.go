// Take some inputs and return as full strings
package render

type CreateContainer struct {
	ContainerName string
	ContainerHost string
	Environment   string
	ImageName     string
	ImageID       string
	Ports         string
	Data          string
	TraefikLabels []string
	Network       string
}

const dockerContainerTemplate = ""
