package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/bluesky-social/indigo/api/atproto"
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

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Unmarshal overwrites only fields present in YAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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

// main()
func main() {
	// Parse command-line flags
	watchOnlyFlag := flag.Bool("watch", false, "Skip initial download, only watch for new likes")
	limitFlag := flag.Int("limit", -1, "Max files to download (overrides config)")
	flag.Parse()

	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatal(err)
	}

	// Configuration from config.yaml
	handle := cfg.Bsky.Handle
	password := cfg.Bsky.Password
	downloadDir := cfg.Download.Dir
	cacheFile := cfg.Download.CacheFile
	downloadLimit := cfg.Download.Limit

	// Override with flag if provided
	if *limitFlag >= 0 {
		downloadLimit = *limitFlag
	}
	pollIntervalMinutes := cfg.PollIntervalMinutes
	ntfyTopic := cfg.NtfyTopic
	healthPort := cfg.HealthPort

	// Watch only mode: true if --watch flag
	watchOnly := *watchOnlyFlag

	// Validate required environment variables
	if handle == "" || password == "" {
		log.Fatal("BSKY_HANDLE and BSKY_PASSWORD must be set in config.yaml")
	}

	//	Create fetcher
	fetcher, err2 := NewMediaFetcher(handle, password, downloadDir, cacheFile)
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "Error initializing: %v\n", err2)
		os.Exit(1)
	}

	// Get ntfy topic for notifications
	if ntfyTopic != "" {
		fmt.Printf("Notifications enabled via ntfy.sh/%s\n", ntfyTopic)
		notify(ntfyTopic, "LikeLocker started")
	}

	// Start health check server for Uptime Kuma
	if healthPort != "" {
		go func() {
			http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "OK")
			})
			fmt.Printf("Health endpoint listening on :%s/health\n", healthPort)
			if err := http.ListenAndServe(":"+healthPort, nil); err != nil {
				fmt.Fprintf(os.Stderr, "Health server error: %v\n", err)
			}
		}()
	}

	//	Fetch and download media (skip if --watch flag)
	if !watchOnly {
		fmt.Printf("Fetching likes and downloading media (limit: %d files)...\n", downloadLimit)
		if err := fetcher.FetchAndDownload(handle, 50, downloadLimit); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Watch mode - poll every X seconds
	if err := fetcher.WatchLikes(handle, time.Duration(pollIntervalMinutes)*time.Minute, ntfyTopic); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Done!")
}
