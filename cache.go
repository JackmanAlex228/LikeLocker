package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

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
