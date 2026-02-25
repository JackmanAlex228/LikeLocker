package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bluesky-social/indigo/api/bsky"
)

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
