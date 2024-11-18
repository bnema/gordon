package verify

import (
	"fmt"
	"os"
)

// VerifyTarFile checks if the file is a valid tar archive
func VerifyTarFile(filepath string) error {
	file, err := os.Open(filepath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Read first few bytes to check tar magic number
	header := make([]byte, 512)
	_, err = file.Read(header)
	if err != nil {
		return fmt.Errorf("failed to read header: %w", err)
	}

	// Check for ustar format
	if string(header[257:262]) != "ustar" {
		return fmt.Errorf("not a valid tar archive")
	}

	// Reset file pointer
	_, err = file.Seek(0, 0)
	return err
}
