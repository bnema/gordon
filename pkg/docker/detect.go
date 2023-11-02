package docker

import (
	"bufio"
	"os"
	"strings"
)

func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return !os.IsNotExist(err)
}

func checkCgroup() (bool, error) {
	// Open the cgroup file for the init process
	file, err := os.Open("/proc/1/cgroup")
	if err != nil {
		return false, err
	}
	defer file.Close()

	// Scan through the cgroup file looking for docker or kube pod paths
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "docker") || strings.Contains(line, "kubepods") {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}

	return false, nil
}

func IsRunningInContainer() (bool, error) {
	// Check for .dockerenv file
	if fileExists("/.iscontainer") {
		return true, nil
	}

	// Check cgroup for docker or kube pod paths
	inContainer, err := checkCgroup()
	if err != nil {
		return false, err
	}

	return inContainer, nil
}
