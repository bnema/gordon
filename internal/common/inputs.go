package common

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
)

// ReadUserInput reads a string input from the user with a prompt.
// It handles multi-word inputs and properly checks for errors.
func ReadUserInput(prompt string) string {
	fmt.Print(prompt + " ")

	// Use bufio.NewReader for better input handling
	reader := bufio.NewReader(os.Stdin)

	// ReadString reads until newline, handling spaces in input
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("Error reading input: %v", err)
		return ""
	}

	// Trim spaces and newline characters
	return strings.TrimSpace(input)
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

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("Error reading input: %v", err)
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
