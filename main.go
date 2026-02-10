package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/joho/godotenv"
)

type MediaFetcher struct {
	client          *xrpc.Client
	downloadDir     string          // Where files are saved (external drive)
	cacheFile       string          // Local cache file tracking downloads
	downloadedFiles map[string]bool // In-memory cache
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

			embed := post.Post.Embed

			// Handle different embed types by checking which field is populated
			if embed.EmbedImages_View != nil {
				downloaded, err := mf.downloadImages(embed.EmbedImages_View.Images, downloadLimit-downloadCount)
				downloadCount += downloaded
				if err != nil {
					fmt.Printf("Error downloading images: %v\n", err)
				}
			}

			if embed.EmbedVideo_View != nil && downloadCount < downloadLimit {
				downloaded, err := mf.downloadVideo(embed.EmbedVideo_View)
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
						downloaded, err := mf.downloadImages(media.EmbedImages_View.Images, downloadLimit-downloadCount)
						downloadCount += downloaded
						if err != nil {
							fmt.Printf("Error downloading images: %v\n", err)
						}
					}
					if media.EmbedVideo_View != nil && downloadCount < downloadLimit {
						downloaded, err := mf.downloadVideo(media.EmbedVideo_View)
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
			fmt.Printf("Error fetching likes: %v\n", err)
			continue
		}

		for _, post := range resp.Feed {
			if seen[post.Post.Uri] {
				continue
			}
			seen[post.Post.Uri] = true
			fmt.Printf("New like: %s\n", post.Post.Uri)

			downloaded, err := mf.downloadPostMedia(post.Post.Embed)
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
func (mf *MediaFetcher) downloadPostMedia(embed *bsky.FeedDefs_PostView_Embed) (int, error) {
	if embed == nil {
		return 0, nil
	}

	downloaded := 0

	if embed.EmbedImages_View != nil {
		n, err := mf.downloadImages(embed.EmbedImages_View.Images, len(embed.EmbedImages_View.Images))
		downloaded += n
		if err != nil {
			return downloaded, err
		}
	}

	if embed.EmbedVideo_View != nil {
		n, err := mf.downloadVideo(embed.EmbedVideo_View)
		downloaded += n
		if err != nil {
			return downloaded, err
		}
	}

	if embed.EmbedRecordWithMedia_View != nil && embed.EmbedRecordWithMedia_View.Media != nil {
		media := embed.EmbedRecordWithMedia_View.Media
		if media.EmbedImages_View != nil {
			n, err := mf.downloadImages(media.EmbedImages_View.Images, len(media.EmbedImages_View.Images))
			downloaded += n
			if err != nil {
				return downloaded, err
			}
		}
		if media.EmbedVideo_View != nil {
			n, err := mf.downloadVideo(media.EmbedVideo_View)
			downloaded += n
			if err != nil {
				return downloaded, err
			}
		}
	}

	return downloaded, nil
}

// MediaFetcher : downloadImages(images []bsky.FeedDefs_FeedViewPost, limit int) : (int, error)
func (mf *MediaFetcher) downloadImages(images []*bsky.EmbedImages_ViewImage, limit int) (int, error) {
	downloadCount := 0
	for _, img := range images {
		if downloadCount >= limit {
			break
		}
		downloaded, err := mf.downloadFile(img.Fullsize, "image")
		if err != nil {
			return downloadCount, err
		}
		downloadCount += downloaded
	}
	return downloadCount, nil
}

// MediaFetcher : downloadVideo(video bsky.EmbedVideo_View) : (int, error)
// Uses ffmpeg to download HLS stream and convert to mp4
func (mf *MediaFetcher) downloadVideo(video *bsky.EmbedVideo_View) (int, error) {
	if video.Playlist == "" {
		return 0, nil
	}

	// Generate filename from URL hash
	hash := sha256.Sum256([]byte(video.Playlist))
	cacheKey := hex.EncodeToString(hash[:])
	filename := cacheKey + ".mp4"
	outputPath := filepath.Join(mf.downloadDir, filename)

	// Check if already downloaded
	if mf.isDownloaded(filename) {
		fmt.Printf("Cache hit: %s\n", filename)
		return 0, nil
	}

	fmt.Printf("Downloading video via ffmpeg: %s\n", video.Playlist)

	// Use ffmpeg to download and convert HLS stream to mp4
	cmd := exec.Command("ffmpeg", "-i", video.Playlist, "-c", "copy", "-y", outputPath)
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

// MediaFetcher : downloadFile(url, mediaType string) : (int, error)
func (mf *MediaFetcher) downloadFile(url, mediaType string) (int, error) {
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
	filename := cacheKey + ext
	filepath := filepath.Join(mf.downloadDir, filename)
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
	out, err := os.Create(filepath)
	if err != nil {
		return 0, fmt.Errorf("failed to create file: %w", err)
	}
	defer out.Close()
	//	Write to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to write file: %w", err)
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
	flag.Parse()

	// Load environment variables from .env file (optional - Docker passes env vars directly)
	_ = godotenv.Load() // Ignore error if .env doesn't exist

	//	Configuration from environment variables
	handle := os.Getenv("BSKY_HANDLE")
	password := os.Getenv("BSKY_PASSWORD")
	downloadDir := os.Getenv("DOWNLOAD_DIR")
	cacheFile := os.Getenv("CACHE_FILE")
	downloadLimitStr := os.Getenv("DOWNLOAD_LIMIT")
	pollIntervalMinutes := os.Getenv("POLL_INTERVAL_MINUTES")
	watchOnlyEnv := os.Getenv("WATCH_ONLY")

	// Watch only mode: true if --watch flag OR WATCH_ONLY=true
	watchOnly := *watchOnlyFlag || watchOnlyEnv == "true"

	// Validate required environment variables
	if handle == "" || password == "" {
		log.Fatal("BSKY_HANDLE and BSKY_PASSWORD must be set in .env file")
	}

	// Set defaults if not specified
	if downloadDir == "" {
		downloadDir = "./downloaded_files"
	}
	if cacheFile == "" {
		cacheFile = "./downloaded_cache.txt"
	}
	if downloadLimitStr == "" {
		downloadLimitStr = "100"
	}
	if pollIntervalMinutes == "" {
		pollIntervalMinutes = "30"
	}

	// Parse download limit
	downloadLimit, err := strconv.Atoi(downloadLimitStr)
	if err != nil {
		log.Fatalf("Invalid DOWNLOAD_LIMIT value: %v", err)
	}

	// Parse poll interval (in seconds)
	pollIntervalMin, err := strconv.Atoi(pollIntervalMinutes)
	if err != nil {
		log.Fatalf("Invalid POLL_INTERVAL value: %v", err)
	}

	//	Create fetcher
	fetcher, err2 := NewMediaFetcher(handle, password, downloadDir, cacheFile)
	if err2 != nil {
		fmt.Fprintf(os.Stderr, "Error initializing: %v\n", err2)
		os.Exit(1)
	}

	// Get ntfy topic for notifications
	ntfyTopic := os.Getenv("NTFY_TOPIC")
	if ntfyTopic != "" {
		fmt.Printf("Notifications enabled via ntfy.sh/%s\n", ntfyTopic)
		notify(ntfyTopic, "LikeLocker started")
	}

	// Start health check server for Uptime Kuma
	healthPort := os.Getenv("HEALTH_PORT")
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

	//	Fetch and download media (skip if --watch flag or WATCH_ONLY env is set)
	if !watchOnly {
		fmt.Printf("Fetching likes and downloading media (limit: %d files)...\n", downloadLimit)
		if err := fetcher.FetchAndDownload(handle, 50, downloadLimit); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Watch mode - poll every X seconds
	if err := fetcher.WatchLikes(handle, time.Duration(pollIntervalMin)*time.Minute, ntfyTopic); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Done!")
}
