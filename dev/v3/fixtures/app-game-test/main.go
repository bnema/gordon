package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var tcpPorts = []string{"27015", "27016", "27017"}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "probe" {
		if err := probe(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var waitGroup sync.WaitGroup
	for _, port := range tcpPorts {
		waitGroup.Add(1)
		go serveTCP(ctx, &waitGroup, port)
	}
	waitGroup.Add(1)
	go serveUDP(ctx, &waitGroup, "27015")
	<-ctx.Done()
	waitGroup.Wait()
}

func serveTCP(ctx context.Context, waitGroup *sync.WaitGroup, port string) {
	defer waitGroup.Done()
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Printf("tcp/%s: %v", port, err)
		return
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("tcp/%s accept: %v", port, err)
			}
			return
		}
		go handleTCP(connection, port)
	}
}

func handleTCP(connection net.Conn, port string) {
	defer connection.Close()
	recordEvent("tcp", port, connection.RemoteAddr().String())
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	payload, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(connection, "protocol=tcp port=%s source=%s payload=%s\n", port, connection.RemoteAddr(), strings.TrimSpace(payload)); err != nil {
		log.Printf("write TCP response: %v", err)
	}
}

func serveUDP(ctx context.Context, waitGroup *sync.WaitGroup, port string) {
	defer waitGroup.Done()
	connection, err := net.ListenPacket("udp", ":"+port)
	if err != nil {
		log.Printf("udp/%s: %v", port, err)
		return
	}
	defer connection.Close()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()
	buffer := make([]byte, 2048)
	for {
		length, source, err := connection.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("udp/%s read: %v", port, err)
			}
			return
		}
		recordEvent("udp", port, source.String())
		response := fmt.Sprintf("protocol=udp port=%s source=%s payload=%s", port, source, strings.TrimSpace(string(buffer[:length])))
		if _, err := connection.WriteTo([]byte(response), source); err != nil {
			log.Printf("udp/%s write: %v", port, err)
		}
	}
}

func recordEvent(protocol, port, source string) {
	file, err := os.OpenFile("/data/events.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("record event: %v", err)
		return
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%s protocol=%s port=%s source=%s\n", time.Now().UTC().Format(time.RFC3339Nano), protocol, port, source); err != nil {
		log.Printf("write event: %v", err)
	}
}

func probe(address string) error {
	connection, err := net.DialTimeout("tcp", address, 3*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(connection, "private-rcon-probe"); err != nil {
		return err
	}
	response, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		return err
	}
	fmt.Print(response)
	return nil
}
