package handler

import (
	"fmt"
	"regexp"
	"strings"
)

func ValidateImageName(imageName string) error {
	match, _ := regexp.MatchString("^([a-zA-Z0-9_\\-\\.]+\\/)*[a-zA-Z0-9_\\-\\.]+(:[a-zA-Z0-9_\\-\\.]+)?$", imageName)
	if !match {
		return fmt.Errorf("You must specify a valid image name in the form (registry/)image:tag, check your container engine image list")
	}
	return nil
}

func EnsureImageTag(imageName *string) {
	if !strings.Contains(*imageName, ":") {
		*imageName += ":latest"
	}
}

func ValidatePortMapping(port string) error {
	match, _ := regexp.MatchString("^[0-9]+:[0-9]+(\\/(tcp|udp))?$", port)
	if !match {
		return fmt.Errorf("You must specify a port mapping in the form port:port/proto, if no protocol is specified, TCP is used")
	}
	return nil
}

func ValidateTargetDomain(targetDomain string) error {
	match, _ := regexp.MatchString("^(https?:\\/\\/)?([a-zA-Z0-9\\-_\\.]+\\.)+[a-zA-Z0-9\\-_\\.]+(:[0-9]+)?$", targetDomain)
	if !match {
		return fmt.Errorf("invalid target domain format")
	}
	return nil
}
