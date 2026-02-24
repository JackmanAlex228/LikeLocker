package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"gopkg.in/yaml.v3"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/xrpc"
)

type MediaFetcher struct {
	client          *xrpc.Client
	downloadDir     string          // Where files are saved (external drive)
	cacheFile       string          // Local cache file tracking downloads
	downloadedFiles map[string]bool // In-memory cache
	refreshMutex 		sync.Mutex			// protects token refresh
}

type Config struct {
	Bsky struct {
		Handle 						string	`yaml:"handle"`
		Password 					string	`yaml:"password`
	} `yaml:"bsky"`
	Download struct {
		Dir								string 	`yaml:"dir"`
		CacheFile					string	`yaml:"cache_file"`
		Limit							int			`yaml:"limit"`
	} `yaml:"download"`
	PollIntervalMinutes int			`yaml:"pol_interval_minutes"`
	NtfyTopic						string	`yaml:"ntfy_topic"`
	HealthPort					string	`yaml:"health_port"`
}

func loadConfig(path string) (*Config, error) {
	// Set defaults
	cfg := Config{
		PollIntervalMinutes:	30,
		HealthPort: 					"8080",
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

// readImageMetadata reads Artist and Source metadata from an image using exiftool
func readImageMetadata(filepath string) (author, postURL string, err error) {
	cmd := exec.Command("exiftool", "-Artist", "-Source", "-s", "-s", "-s", filepath)
	output, err := cmd.Output()
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) >= 1 {
		author = strings.TrimSpace(lines[0])
	}
	if len(lines) >= 2 {
		postURL = strings.TrimSpace(lines[1])
	}
	return author, postURL, nil
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

// NewMediaFetcher(handle, password, downloadDir, cacheFile string) : MediaFetcher!
func NewMediaFetcher(handle, password, downloadDir, cacheFile string) (*MediaFetcher, error) {
	// Create download directory
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create download dir: %w", err)
	}
	// 	Create XRPC client
	client := &xrpc.Client{
		Host: "https://bsky.social",
	}
	// 	Authenticate
	fmt.Printf("Authenticating bsky user %s...\n", handle)
	auth, err := atproto.ServerCreateSession(context.Background(), client, &atproto.ServerCreateSession_Input{
		Identifier: handle,
		Password:   password,
	})
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	} else {
		fmt.Printf("Authentication successful!\n")
	}
	// 	Set auth token for subsequent requests
	client.Auth = &xrpc.AuthInfo{
		AccessJwt:  auth.AccessJwt,
		RefreshJwt: auth.RefreshJwt,
		Handle:     auth.Handle,
		Did:        auth.Did,
	}
	mf := &MediaFetcher{
		client:          client,
		downloadDir:     downloadDir,
		cacheFile:       cacheFile,
		downloadedFiles: make(map[string]bool),
	}

	// Load cache from file
	if err := mf.loadCache(); err != nil {
		return nil, fmt.Errorf("failed to load cache: %w", err)
	}

	// Sync cache with existing files in directory
	if err := mf.syncCacheFromDirectory(); err != nil {
		return nil, fmt.Errorf("failed to save cache after sync: %w", err)
	}

	return mf, nil
}

// FetchAndDownload fetches liked posts and downloads media in batches, stopping when downloadLimit is reached
func (mf *MediaFetcher) FetchAndDownload(actor string, batchSize int64, downloadLimit int) error {
	var cursor string
	downloadCount := 0
	postsProcessed := 0

	fmt.Print("\033[s")
	for downloadCount < downloadLimit {
		resp, err := bsky.FeedGetActorLikes(context.Background(), mf.client, actor, cursor, batchSize)
		if err != nil {
			return fmt.Errorf("failed to fetch likes: %w", err)
		}

		// Break if no posts returned
		if len(resp.Feed) == 0 {
			break
		}

		// Process and download from this batch
		for _, post := range resp.Feed {
			if downloadCount >= downloadLimit {
				fmt.Printf("\nReached download limit of %d files\n", downloadLimit)
				fmt.Printf("Total files downloaded: %d\n", downloadCount)
				return nil
			}

			postsProcessed++
			fmt.Print("\033[u\033[K")
			fmt.Printf("Processing post %d (downloaded: %d/%d)\n", postsProcessed, downloadCount, downloadLimit)

			// Check if post has embed
			if post.Post.Embed == nil {
				continue
			}

			// Extract author info and post URL
			author := post.Post.Author.Handle
			postURL := post.Post.Uri

			embed := post.Post.Embed

			// Handle different embed types by checking which field is populated
			if embed.EmbedImages_View != nil {
				downloaded, err := mf.downloadImages(embed.EmbedImages_View.Images, downloadLimit-downloadCount, author, postURL)
				downloadCount += downloaded
				if err != nil {
					fmt.Printf("Error downloading images: %v\n", err)
				}
			}

			if embed.EmbedVideo_View != nil && downloadCount < downloadLimit {
				downloaded, err := mf.downloadVideo(embed.EmbedVideo_View, author, postURL)
				downloadCount += downloaded
				if err != nil {
					fmt.Printf("Error downloading video: %v\n", err)
				}
			}

			if embed.EmbedRecordWithMedia_View != nil && downloadCount < downloadLimit {
				// Handle posts with both record and media
				if embed.EmbedRecordWithMedia_View.Media != nil {
					media := embed.EmbedRecordWithMedia_View.Media
					if media.EmbedImages_View != nil {
						downloaded, err := mf.downloadImages(media.EmbedImages_View.Images, downloadLimit-downloadCount, author, postURL)
						downloadCount += downloaded
						if err != nil {
							fmt.Printf("Error downloading images: %v\n", err)
						}
					}
					if media.EmbedVideo_View != nil && downloadCount < downloadLimit {
						downloaded, err := mf.downloadVideo(media.EmbedVideo_View, author, postURL)
						downloadCount += downloaded
						if err != nil {
							fmt.Printf("Error downloading video: %v\n", err)
						}
					}
				}
			}
		}

		// Break if no more pages
		if resp.Cursor == nil || *resp.Cursor == "" {
			break
		}
		cursor = *resp.Cursor
	}

	fmt.Printf("\nTotal files downloaded: %d\n", downloadCount)
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
	originalAccess := mf.client.Auth.AccessJwt						// save the ACCESS token
	mf.client.Auth.AccessJwt = mf.client.Auth.RefreshJwt	// swap in the REFRESH token
	
	refreshed, err := atproto.ServerRefreshSession(context.Background(), mf.client)
	if err != nil {
		fmt.Printf("DEBUG refresh error: %w\n", err)
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

// WatchLikes polls for new likes and prints when new media is found
func (mf *MediaFetcher) WatchLikes(actor string, interval time.Duration, ntfyTopic string) error {
	seen := make(map[string]bool)

	// Initial load - mark existing likes as seen
	fmt.Println("Loading existing likes...")
	resp, err := bsky.FeedGetActorLikes(context.Background(), mf.client, actor, "", 50)
	if err != nil {
		return fmt.Errorf("failed to fetch initial likes: %w", err)
	}
	for _, post := range resp.Feed {
		seen[post.Post.Uri] = true
	}
	fmt.Printf("Tracking %d existing likes. Watching for new ones...\n", len(seen))

	for {
		time.Sleep(interval)

		resp, err := bsky.FeedGetActorLikes(context.Background(), mf.client, actor, "", 50)
		if err != nil {
			// Check if token expired and try to refresh
			if strings.Contains(err.Error(), "ExpiredToken") {
				if refreshErr := mf.refreshToken(ntfyTopic); refreshErr != nil {
					fmt.Printf("Error refreshing token: %v\n", refreshErr)
					continue
				}
				// Retry the request after refresh
				resp, err = bsky.FeedGetActorLikes(context.Background(), mf.client, actor, "", 50)
				if err != nil {
					fmt.Printf("Error fetching likes after refresh: %v\n", err)
					continue
				}
			} else {
				fmt.Printf("Error fetching likes: %v\n", err)
				continue
			}
		}

		for _, post := range resp.Feed {
			if seen[post.Post.Uri] {
				continue
			}
			seen[post.Post.Uri] = true
			fmt.Printf("New like: %s\n", post.Post.Uri)

			author := post.Post.Author.Handle
			postURL := post.Post.Uri

			downloaded, err := mf.downloadPostMedia(post.Post.Embed, author, postURL)
			if err != nil {
				fmt.Printf("Error downloading media: %v\n", err)
			} else if downloaded > 0 {
				fmt.Printf("Downloaded %d file(s)\n", downloaded)
				notify(ntfyTopic, fmt.Sprintf("Downloaded %d file(s) from new like", downloaded))
			}
		}
	}
}

// loadCache reads the cache file and populates the downloadedFiles map
func (mf *MediaFetcher) loadCache() error {
	file, err := os.Open(mf.cacheFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No cache file found, starting fresh")
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		filename := strings.TrimSpace(scanner.Text())
		if filename != "" {
			mf.downloadedFiles[filename] = true
			count++
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	fmt.Printf("Cached %d files already downloaded\n", count)
	return nil
}

// saveCache writes the current cache to disk
func (mf *MediaFetcher) saveCache() error {
	file, err := os.Create(mf.cacheFile)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	for filename := range mf.downloadedFiles {
		if _, err := writer.WriteString(filename + "\n"); err != nil {
			return err
		}
	}

	return writer.Flush()
}

// syncCacheFromDirectory scans the download directory and adds any existing files to the cache.
// Useful for recovering from a lost/corrupted cache file or when files were added manually.
func (mf *MediaFetcher) syncCacheFromDirectory() error {
	entries, err := os.ReadDir(mf.downloadDir)
	if err != nil {
		return fmt.Errorf("failed to read download directory: %w", err)
	}

	added := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		if !mf.downloadedFiles[filename] {
			mf.downloadedFiles[filename] = true
			added++
		}
	}
	if added > 0 {
		fmt.Printf("Synced %d files from directory to cache\n", added)
		if err := mf.saveCache(); err != nil {
			return fmt.Errorf("failed to save cache after sync: %w", err)
		}
	}
	return nil
}

// markDownloaded adds a filename to the cache and saves it
func (mf *MediaFetcher) markDownloaded(filename string) error {
	mf.downloadedFiles[filename] = true
	return mf.saveCache()
}

// isDownloaded checks if a file has already been downloaded
func (mf *MediaFetcher) isDownloaded(filename string) bool {
	return mf.downloadedFiles[filename]
}

// downloadPostMedia downloads any media from a single post's embed
func (mf *MediaFetcher) downloadPostMedia(embed *bsky.FeedDefs_PostView_Embed, author, postURL string) (int, error) {
	if embed == nil {
		return 0, nil
	}

	downloaded := 0

	if embed.EmbedImages_View != nil {
		n, err := mf.downloadImages(embed.EmbedImages_View.Images, len(embed.EmbedImages_View.Images), author, postURL)
		downloaded += n
		if err != nil {
			return downloaded, err
		}
	}

	if embed.EmbedVideo_View != nil {
		n, err := mf.downloadVideo(embed.EmbedVideo_View, author, postURL)
		downloaded += n
		if err != nil {
			return downloaded, err
		}
	}

	if embed.EmbedRecordWithMedia_View != nil && embed.EmbedRecordWithMedia_View.Media != nil {
		media := embed.EmbedRecordWithMedia_View.Media
		if media.EmbedImages_View != nil {
			n, err := mf.downloadImages(media.EmbedImages_View.Images, len(media.EmbedImages_View.Images), author, postURL)
			downloaded += n
			if err != nil {
				return downloaded, err
			}
		}
		if media.EmbedVideo_View != nil {
			n, err := mf.downloadVideo(media.EmbedVideo_View, author, postURL)
			downloaded += n
			if err != nil {
				return downloaded, err
			}
		}
	}

	return downloaded, nil
}

// MediaFetcher : downloadImages(images, limit, author, postURL) : (int, error)
func (mf *MediaFetcher) downloadImages(images []*bsky.EmbedImages_ViewImage, limit int, author, postURL string) (int, error) {
	downloadCount := 0
	for _, img := range images {
		if downloadCount >= limit {
			break
		}
		downloaded, err := mf.downloadFile(img.Fullsize, "image", author, postURL)
		if err != nil {
			return downloadCount, err
		}
		downloadCount += downloaded
	}
	return downloadCount, nil
}

// MediaFetcher : downloadVideo(video, author, postURL) : (int, error)
// Uses ffmpeg to download HLS stream and convert to mp4 with embedded metadata
func (mf *MediaFetcher) downloadVideo(video *bsky.EmbedVideo_View, author, postURL string) (int, error) {
	if video.Playlist == "" {
		return 0, nil
	}

	// Generate filename from URL hash with author prefix
	hash := sha256.Sum256([]byte(video.Playlist))
	cacheKey := hex.EncodeToString(hash[:])
	sanitizedAuthor := sanitizeForFilename(author)
	filename := sanitizedAuthor + "_" + cacheKey + ".mp4"
	outputPath := filepath.Join(mf.downloadDir, filename)

	// Check if already downloaded
	if mf.isDownloaded(filename) {
		fmt.Printf("Cache hit: %s\n", filename)
		return 0, nil
	}

	fmt.Printf("Downloading video via ffmpeg: %s\n", video.Playlist)

	// Use ffmpeg to download and convert HLS stream to mp4 with metadata
	cmd := exec.Command("ffmpeg",
		"-i", video.Playlist,
		"-c", "copy",
		"-metadata", "artist="+author,
		"-metadata", "comment="+postURL,
		"-y", outputPath)
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffmpeg failed: %w", err)
	}

	// Mark as downloaded in cache
	if err := mf.markDownloaded(filename); err != nil {
		fmt.Printf("Warning: failed to update cache: %v\n", err)
	}

	fmt.Printf("Saved: %s\n", filename)
	return 1, nil
}

// MediaFetcher : downloadFile(url, mediaType, author, postURL string) : (int, error)
func (mf *MediaFetcher) downloadFile(url, mediaType, author, postURL string) (int, error) {
	//	Generate cache key from URL
	hash := sha256.Sum256([]byte(url))
	cacheKey := hex.EncodeToString(hash[:])
	//	Determine file extension
	ext := filepath.Ext(url)
	if ext == "" {
		if strings.Contains(url, "m3u8") {
			ext = ".m3u8"
		} else if mediaType == "image" {
			ext = ".png"
		} else {
			ext = ".mp4"
		}
	}
	// Build filename with author prefix
	sanitizedAuthor := sanitizeForFilename(author)
	filename := sanitizedAuthor + "_" + cacheKey + ext
	filePath := filepath.Join(mf.downloadDir, filename)
	//	Check if already cached
	if mf.isDownloaded(filename) {
		fmt.Printf("Cache hit: %s\n", filename)
		return 0, nil // Return 0 because we didn't download a new file
	}
	fmt.Printf("Downloading: %s\n", url)
	//	Download file
	resp, err := http.Get(url)
	if err != nil {
		return 0, fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("bad status: %s", resp.Status)
	}
	//	Create file
	out, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()
	//	Write to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to write file: %w", err)
	}

	// Embed metadata in images
	if mediaType == "image" {
		if err := embedImageMetadata(filePath, author, postURL); err != nil {
			fmt.Printf("Warning: failed to embed metadata: %v\n", err)
		}
	}

	// Mark as downloaded in cache
	if err := mf.markDownloaded(filename); err != nil {
		fmt.Printf("Warning: failed to update cache: %v\n", err)
	}

	fmt.Printf("Saved: %s\n", filename)
	return 1, nil // Return 1 because we successfully downloaded a new file
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

	// Migrate existing files to new format with author prefix
	if err := fetcher.migrateExistingFiles(handle); err != nil {
		fmt.Printf("Warning: migration encountered errors: %v\n", err)
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
