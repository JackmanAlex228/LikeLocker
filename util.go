package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/bluesky-social/indigo/api/atproto"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Bsky struct {
		Handle   string `yaml:"handle"`
		Password string `yaml:"password"`
	} `yaml:"bsky"`
	Download struct {
		Dir       string `yaml:"dir"`
		CacheFile string `yaml:"cache_file"`
		Limit     int    `yaml:"limit"`
	} `yaml:"download"`
	PollIntervalMinutes int    `yaml:"poll_interval_minutes"`
	NtfyTopic           string `yaml:"ntfy_topic"`
	HealthPort          string `yaml:"health_port"`
}

func loadConfig(path string) (*Config, error) {
	// Set defaults
	cfg := Config{
		PollIntervalMinutes: 30,
		HealthPort:          "8080",
	}
	cfg.Download.Limit = 100
	cfg.Download.Dir = "./downloaded_files"
	cfg.Download.CacheFile = "./downloaded_cache.txt"

	// If config doesn't exist, copy from example
	if _, err := os.Stat(path); os.IsNotExist(err) {
		examplePath := "config.example.yaml"
		if exampleData, err := os.ReadFile(examplePath); err == nil {
			if err := os.WriteFile(path, exampleData, 0600); err != nil {
				fmt.Printf("Warning: could not create %s: %v\n", path, err)
			} else {
				fmt.Printf("Created %s from %s\n", path, examplePath)
			}
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Config file still doesn't exist, return defaults
			return &cfg, nil
		}
		return nil, err
	}

	// Unmarshal overwrites only fields present in YAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func promptCredentials(cfg *Config) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter your Bluesky handle (e.g., user.bsky.social): ")
	handle, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	cfg.Bsky.Handle = strings.TrimSpace(handle)

	fmt.Print("Enter your Bluesky app password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	fmt.Println() // newline after hidden input
	cfg.Bsky.Password = string(passwordBytes)

	return nil
}

// refreshToken refreshes the access token using the refresh token
func (mf *MediaFetcher) refreshToken(ntfyTopic string) error {
	// Mutex lock for handeling race conditions
	mf.refreshMutex.Lock()
	defer mf.refreshMutex.Unlock()

	fmt.Println("Refreshing authentication token...")
	notify(ntfyTopic, "Refreshing authentication token..")

	// ServerRefreshSession requires refresh token as bearer, not access token
	originalAccess := mf.client.Auth.AccessJwt           // save the ACCESS token
	mf.client.Auth.AccessJwt = mf.client.Auth.RefreshJwt // swap in the REFRESH token

	refreshed, err := atproto.ServerRefreshSession(context.Background(), mf.client)
	if err != nil {
		fmt.Printf("DEBUG refresh error: %v\n", err)
		mf.client.Auth.AccessJwt = originalAccess // restore original access token
		notify(ntfyTopic, fmt.Sprintf("failed to refresh token: %v", err))
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	mf.client.Auth.AccessJwt = refreshed.AccessJwt
	mf.client.Auth.RefreshJwt = refreshed.RefreshJwt
	notify(ntfyTopic, "Token refreshed successfully")
	fmt.Println("Token refreshed successfully")
	return nil
}

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
