package common

import "time"

const checkVersionURL = "https://gordon.bnema.dev/version"

var timer = time.NewTicker(1 * time.Minute)

type CurrentVersion struct {
	isDocker bool
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Sha256ID string `json:"sha256id"`
}

type Response interface{}
