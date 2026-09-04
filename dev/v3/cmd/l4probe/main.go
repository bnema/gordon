package main

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 5 {
		fatal("usage: l4probe <tcp|udp> <address> <payload> <expected-source-ip>")
	}
	protocol, address, payload, expectedSource := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	if protocol == "closed" {
		connection, err := net.DialTimeout("tcp", address, 3*time.Second)
		if err == nil {
			connection.Close()
			fatal("private port reachable: " + address)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			fatal("cannot establish private-port refusal: " + err.Error())
		}
		fmt.Println("private port refused: " + address)
		return
	}
	response, err := exchange(protocol, address, payload)
	if err != nil {
		fatal(err.Error())
	}
	if !strings.Contains(response, "protocol="+protocol) || !strings.Contains(response, "payload="+payload) || !strings.Contains(response, "source="+expectedSource+":") {
		fatal("unexpected response: " + response)
	}
	fmt.Println(response)
}

func exchange(protocol, address, payload string) (string, error) {
	connection, err := net.DialTimeout(protocol, address, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(connection, payload); err != nil {
		return "", err
	}
	if protocol == "udp" {
		buffer := make([]byte, 2048)
		n, err := connection.Read(buffer)
		return strings.TrimSpace(string(buffer[:n])), err
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(response), nil
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
