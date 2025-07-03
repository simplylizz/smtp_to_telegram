package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadBlacklist(t *testing.T) {
	// Create a temporary blacklist file
	tmpfile, err := os.CreateTemp("", "blacklist_test*.txt")
	assert.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	// Write test data
	content := `# Test blacklist
spam@example.com
UPPERCASE@TEST.COM
  spaced@email.com  
# comment line
valid@email.com
`
	_, err = tmpfile.WriteString(content)
	assert.NoError(t, err)
	tmpfile.Close()

	// Load the blacklist
	err = loadBlacklist(tmpfile.Name())
	assert.NoError(t, err)

	// Test blacklisted emails
	assert.True(t, isBlacklisted("spam@example.com"))
	assert.True(t, isBlacklisted("SPAM@EXAMPLE.COM")) // Case insensitive
	assert.True(t, isBlacklisted("uppercase@test.com"))
	assert.True(t, isBlacklisted("UPPERCASE@TEST.COM"))
	assert.True(t, isBlacklisted("spaced@email.com"))
	assert.True(t, isBlacklisted("  spaced@email.com  ")) // With spaces
	assert.True(t, isBlacklisted("valid@email.com"))

	// Test non-blacklisted emails
	assert.False(t, isBlacklisted("good@example.com"))
	assert.False(t, isBlacklisted(""))
}

func TestLoadBlacklistEmptyFile(t *testing.T) {
	// Test with empty filename
	err := loadBlacklist("")
	assert.NoError(t, err)
	assert.False(t, isBlacklisted("any@email.com"))
}

func TestLoadBlacklistNonExistentFile(t *testing.T) {
	// Test with non-existent file
	err := loadBlacklist("/non/existent/file.txt")
	assert.NoError(t, err) // Should not error, just warn
	assert.False(t, isBlacklisted("any@email.com"))
}
