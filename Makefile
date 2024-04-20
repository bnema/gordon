# Variables
INSTALL_DIR = /usr/bin
BINARY_NAME = gordon 
# Default target
all: build

# Build the Go binary
build:
	go build -o $(BINARY_NAME) cmd/cli/main.go

# Install the binary to /usr/local/bin
install:
	mv $(BINARY_NAME) $(INSTALL_DIR)

# Clean up the built binary
clean:
	rm -f $(BINARY_NAME)

.PHONY: all build install clean