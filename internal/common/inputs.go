package common

import "fmt"

// readUserInput reads a string input from the user with a prompt.
func readUserInput(prompt string) string {
	fmt.Println(prompt)
	var input string
	fmt.Scanln(&input)
	return input
}
