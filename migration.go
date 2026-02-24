package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bluesky-social/indigo/api/bsky"
)

// isOldFormatFilename checks if a filename is in the old format (just hash.ext, no author prefix)
// Old format: 64 hex characters + extension (e.g., "a1b2c3...64chars...d4e5.jpg")
// New format: author_hash.ext (e.g., "username_a1b2c3...d4e5.jpg")
func isOldFormatFilename(filename string) bool {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	// Old format is exactly 64 hex characters (SHA256)
	if len(base) != 64 {
		return false
	}
	for _, c := range base {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// migrateExistingFiles migrates old-format files to new format with author prefix
// It reads metadata from files if available, or matches against current likes
func (mf *MediaFetcher) migrateExistingFiles(actor string) error {
	entries, err := os.ReadDir(mf.downloadDir)
	if err != nil {
		return fmt.Errorf("failed to read download directory: %w", err)
	}

	// Find old-format files that need migration
	var oldFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isOldFormatFilename(entry.Name()) {
			oldFiles = append(oldFiles, entry.Name())
		}
	}

	if len(oldFiles) == 0 {
		return nil
	}

	fmt.Printf("Found %d files in old format, attempting migration...\n", len(oldFiles))

	// First, try to migrate files that have embedded metadata
	migratedFromMetadata := 0
	remainingFiles := make([]string, 0)

	for _, filename := range oldFiles {
		filePath := filepath.Join(mf.downloadDir, filename)
		ext := strings.ToLower(filepath.Ext(filename))

		// Try to read metadata from images
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
			author, _, err := readImageMetadata(filePath)
			if err == nil && author != "" {
				// Rename file with author prefix
				newFilename := sanitizeForFilename(author) + "_" + filename
				newPath := filepath.Join(mf.downloadDir, newFilename)
				if err := os.Rename(filePath, newPath); err != nil {
					fmt.Printf("Warning: failed to rename %s: %v\n", filename, err)
					continue
				}
				// Update cache
				delete(mf.downloadedFiles, filename)
				mf.downloadedFiles[newFilename] = true
				migratedFromMetadata++
				continue
			}
		}

		// For videos, try ffprobe to read metadata
		if ext == ".mp4" || ext == ".mov" || ext == ".webm" {
			cmd := exec.Command("ffprobe", "-v", "quiet", "-show_entries", "format_tags=artist", "-of", "default=noprint_wrappers=1:nokey=1", filePath)
			output, err := cmd.Output()
			if err == nil {
				author := strings.TrimSpace(string(output))
				if author != "" {
					newFilename := sanitizeForFilename(author) + "_" + filename
					newPath := filepath.Join(mf.downloadDir, newFilename)
					if err := os.Rename(filePath, newPath); err != nil {
						fmt.Printf("Warning: failed to rename %s: %v\n", filename, err)
						continue
					}
					delete(mf.downloadedFiles, filename)
					mf.downloadedFiles[newFilename] = true
					migratedFromMetadata++
					continue
				}
			}
		}

		remainingFiles = append(remainingFiles, filename)
	}

	if migratedFromMetadata > 0 {
		fmt.Printf("Migrated %d files using embedded metadata\n", migratedFromMetadata)
	}

	// For remaining files, try to match against current likes
	if len(remainingFiles) > 0 {
		fmt.Printf("Attempting to match %d remaining files against current likes...\n", len(remainingFiles))
		migratedFromLikes, err := mf.migrateFromLikes(actor, remainingFiles)
		if err != nil {
			fmt.Printf("Warning: error during likes matching: %v\n", err)
		} else if migratedFromLikes > 0 {
			fmt.Printf("Migrated %d files by matching against likes\n", migratedFromLikes)
		}
	}

	// Save updated cache
	if err := mf.saveCache(); err != nil {
		return fmt.Errorf("failed to save cache after migration: %w", err)
	}

	return nil
}

// migrateFromLikes fetches all likes and tries to match old-format files
func (mf *MediaFetcher) migrateFromLikes(actor string, oldFiles []string) (int, error) {
	// Build a map of hash -> filename for quick lookup
	hashToFile := make(map[string]string)
	for _, filename := range oldFiles {
		ext := filepath.Ext(filename)
		hash := strings.TrimSuffix(filename, ext)
		hashToFile[hash] = filename
	}

	migrated := 0
	var cursor string

	for {
		resp, err := bsky.FeedGetActorLikes(context.Background(), mf.client, actor, cursor, 100)
		if err != nil {
			return migrated, fmt.Errorf("failed to fetch likes: %w", err)
		}

		if len(resp.Feed) == 0 {
			break
		}

		for _, post := range resp.Feed {
			if post.Post.Embed == nil {
				continue
			}

			author := post.Post.Author.Handle
			postURL := post.Post.Uri
			embed := post.Post.Embed

			// Check images
			if embed.EmbedImages_View != nil {
				for _, img := range embed.EmbedImages_View.Images {
					hash := sha256.Sum256([]byte(img.Fullsize))
					hashStr := hex.EncodeToString(hash[:])
					if filename, ok := hashToFile[hashStr]; ok {
						if err := mf.migrateFile(filename, author, postURL, "image"); err != nil {
							fmt.Printf("Warning: failed to migrate %s: %v\n", filename, err)
						} else {
							delete(hashToFile, hashStr)
							migrated++
						}
					}
				}
			}

			// Check videos
			if embed.EmbedVideo_View != nil && embed.EmbedVideo_View.Playlist != "" {
				hash := sha256.Sum256([]byte(embed.EmbedVideo_View.Playlist))
				hashStr := hex.EncodeToString(hash[:])
				if filename, ok := hashToFile[hashStr]; ok {
					if err := mf.migrateFile(filename, author, postURL, "video"); err != nil {
						fmt.Printf("Warning: failed to migrate %s: %v\n", filename, err)
					} else {
						delete(hashToFile, hashStr)
						migrated++
					}
				}
			}

			// Check record with media
			if embed.EmbedRecordWithMedia_View != nil && embed.EmbedRecordWithMedia_View.Media != nil {
				media := embed.EmbedRecordWithMedia_View.Media
				if media.EmbedImages_View != nil {
					for _, img := range media.EmbedImages_View.Images {
						hash := sha256.Sum256([]byte(img.Fullsize))
						hashStr := hex.EncodeToString(hash[:])
						if filename, ok := hashToFile[hashStr]; ok {
							if err := mf.migrateFile(filename, author, postURL, "image"); err != nil {
								fmt.Printf("Warning: failed to migrate %s: %v\n", filename, err)
							} else {
								delete(hashToFile, hashStr)
								migrated++
							}
						}
					}
				}
				if media.EmbedVideo_View != nil && media.EmbedVideo_View.Playlist != "" {
					hash := sha256.Sum256([]byte(media.EmbedVideo_View.Playlist))
					hashStr := hex.EncodeToString(hash[:])
					if filename, ok := hashToFile[hashStr]; ok {
						if err := mf.migrateFile(filename, author, postURL, "video"); err != nil {
							fmt.Printf("Warning: failed to migrate %s: %v\n", filename, err)
						} else {
							delete(hashToFile, hashStr)
							migrated++
						}
					}
				}
			}
		}

		// Check if we've found all files
		if len(hashToFile) == 0 {
			break
		}

		if resp.Cursor == nil || *resp.Cursor == "" {
			break
		}
		cursor = *resp.Cursor
	}

	if len(hashToFile) > 0 {
		fmt.Printf("Could not find author info for %d files (posts may have been unliked or deleted)\n", len(hashToFile))
	}

	return migrated, nil
}

// migrateFile renames a file to include author prefix and embeds metadata
func (mf *MediaFetcher) migrateFile(filename, author, postURL, mediaType string) error {
	oldPath := filepath.Join(mf.downloadDir, filename)
	newFilename := sanitizeForFilename(author) + "_" + filename
	newPath := filepath.Join(mf.downloadDir, newFilename)

	// Embed metadata first (before rename)
	if mediaType == "image" {
		if err := embedImageMetadata(oldPath, author, postURL); err != nil {
			fmt.Printf("Warning: failed to embed metadata in %s: %v\n", filename, err)
		}
	} else if mediaType == "video" {
		// For videos, we need to re-encode to add metadata (ffmpeg can't edit in place)
		// Create a temp file with metadata
		tempPath := oldPath + ".tmp.mp4"
		cmd := exec.Command("ffmpeg", "-i", oldPath, "-c", "copy",
			"-metadata", "artist="+author,
			"-metadata", "comment="+postURL,
			"-y", tempPath)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Run(); err != nil {
			os.Remove(tempPath)
			fmt.Printf("Warning: failed to embed metadata in video %s: %v\n", filename, err)
		} else {
			os.Remove(oldPath)
			os.Rename(tempPath, oldPath)
		}
	}

	// Rename file
	if err := os.Rename(oldPath, newPath); err != nil {
		return err
	}

	// Update cache
	delete(mf.downloadedFiles, filename)
	mf.downloadedFiles[newFilename] = true

	return nil
}
