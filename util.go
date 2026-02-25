package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// sanitizeForFilename removes or replaces characters that are invalid in filenames
func sanitizeForFilename(s string) string {
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// embedImageMetadata uses exiftool to add metadata to an image
func embedImageMetadata(filepath, author, postURL string) error {
	cmd := exec.Command("exiftool",
		"-overwrite_original",
		"-Artist="+author,
		"-Source="+postURL,
		filepath)
	return cmd.Run()
}

// notify sends a push notification via ntfy.sh (if topic is configured)
func notify(topic, message string) {
	if topic == "" {
		return
	}
	resp, err := http.Post("https://ntfy.sh/"+topic, "text/plain", strings.NewReader(message))
	if err != nil {
		fmt.Printf("Warning: failed to send notification: %v\n", err)
		return
	}
	resp.Body.Close()
}
