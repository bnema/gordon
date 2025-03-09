package common

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/log"
)

// ReadUserInput reads a string input from the user with a prompt.
// It handles multi-word inputs and properly checks for errors.
func ReadUserInput(prompt string) string {
	log.Debug("ReadUserInput: Prompting user for input", "prompt", prompt)
	// Make the prompt more visible with newlines and formatting
	fmt.Printf("\n>>> %s: ", prompt)
	// Flush to ensure the prompt is displayed
	os.Stdout.Sync()

	// Use bufio.NewReader for better input handling
	reader := bufio.NewReader(os.Stdin)
	log.Debug("ReadUserInput: Created reader, waiting for input...")

	// ReadString reads until newline, handling spaces in input
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Error("ReadUserInput: Error reading input", "error", err)
		return ""
	}

	// Trim spaces and newline characters
	result := strings.TrimSpace(input)
	log.Debug("ReadUserInput: Received input", "value", result)
	return result
}

// ReadUserInputWithValidation reads user input and validates it using a provided function.
// It keeps prompting until valid input is received.
func ReadUserInputWithValidation(prompt string, validate func(string) bool) string {
	for {
		input := ReadUserInput(prompt)
		if validate(input) {
			return input
		}
		fmt.Println("Invalid input. Please try again.")
	}
}

// ReadUserInputWithDefault reads user input with a default value if input is empty.
func ReadUserInputWithDefault(prompt string, defaultValue string) string {
	fmt.Printf("%s (default: %s) ", prompt, defaultValue)
	// Flush to ensure the prompt is displayed
	os.Stdout.Sync()

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Error("Error reading input", "error", err)
		return defaultValue
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return defaultValue
	}
	return input
}

// ReadConfirmation reads a yes/no confirmation from the user.
func ReadConfirmation(prompt string) bool {
	input := ReadUserInputWithValidation(
		prompt+" (y/n)",
		func(s string) bool {
			s = strings.ToLower(s)
			return s == "y" || s == "n" || s == "yes" || s == "no"
		},
	)

	input = strings.ToLower(input)
	return input == "y" || input == "yes"
}

// ReadUserInputNonBlocking attempts to read user input but doesn't block in non-interactive environments.
// Instead, it returns the provided default value when not in an interactive terminal.
func ReadUserInputNonBlocking(prompt string, defaultValue string) string {
	// Check if we're in an interactive terminal
	stat, err := os.Stdin.Stat()
	if err != nil {
		log.Error("ReadUserInputNonBlocking: Error checking terminal status", "error", err)
		log.Info("Defaulting to non-interactive mode due to error", "default", defaultValue)
		return defaultValue
	}

	isTerminal := (stat.Mode() & os.ModeCharDevice) != 0
	log.Debug("ReadUserInputNonBlocking: Terminal status check",
		"isInteractive", isTerminal,
		"fileMode", stat.Mode().String())

	if isTerminal {
		// In interactive terminal, use normal prompt
		log.Debug("ReadUserInputNonBlocking: Using interactive prompt")
		return ReadUserInput(prompt)
	} else {
		// Not in interactive terminal, log and return default
		log.Info("Non-interactive environment, using default for prompt",
			"prompt", prompt,
			"default", defaultValue)
		return defaultValue
	}
}
