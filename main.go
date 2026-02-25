package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

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

	// Prompt for credentials if not configured
	if handle == "" || password == "" {
		fmt.Println("Bluesky credentials not found in config.yaml")
		if err := promptCredentials(cfg); err != nil {
			log.Fatalf("Failed to read credentials: %v", err)
		}
		handle = cfg.Bsky.Handle
		password = cfg.Bsky.Password

		// Save credentials to config file
		if err := saveConfig("config.yaml", cfg); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		fmt.Println("Credentials saved to config.yaml")
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
