package main

import (
	"context"
	"fmt"
	"os"
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
	refreshMutex    sync.Mutex      // protects token refresh
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
