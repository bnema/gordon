package common

import "fmt"

// ReadUserInput reads a string input from the user with a prompt.
func ReadUserInput(prompt string) string {
	fmt.Println(prompt)
	var input string
	fmt.Scanln(&input)
	return input
}
