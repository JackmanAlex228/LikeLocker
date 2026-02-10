# LikeLocker

LikeLocker downloads and archives media (images and videos) from your liked posts on social media platforms. Your likes represent content you found valuable enough to save - this tool helps you actually keep it before it disappears.

Currently supports **Bluesky**. More platforms planned.

## Features

- Downloads images and videos from liked posts
- Incremental downloads with local cache (only fetches new content)
- Configurable download limits per run
- Video support via ffmpeg (handles HLS streams)
- Watch mode for continuous monitoring of new likes
- Content-addressed filenames (SHA256 hash) to avoid duplicates

## Requirements

- Go 1.24+ (for building from source)
- ffmpeg (for video downloads)
- A Bluesky account with an app password

## Setup

1. Clone and build:
```bash
git clone https://github.com/yourusername/LikeLocker.git
cd LikeLocker
go build -o likelocker .
```

2. Copy the example environment file and configure:
```bash
cp .env.example .env
```

3. Edit `.env` with your Bluesky credentials:
```
BSKY_HANDLE=your-handle.bsky.social
BSKY_PASSWORD=your-app-password
DOWNLOAD_LIMIT=100
```

4. Run:
```bash
./likelocker
```

Downloaded files will appear in `./downloaded_files/`.

## Usage

```bash
# Download liked media up to DOWNLOAD_LIMIT, then watch for new likes
./likelocker

# Skip initial download, only watch for new likes
./likelocker --watch
```

### Development

For testing and debugging, you can run directly with Go without building:

```bash
# Run directly
go run main.go

# Run with watch flag
go run main.go --watch
```

The app runs in two phases:

1. **Initial download** - Fetches your existing likes and downloads media up to `DOWNLOAD_LIMIT`
2. **Watch mode** - Polls for new likes every `POLL_INTERVAL` seconds and downloads new media

Use `--watch` (or `WATCH_ONLY=true`) to skip phase 1 and go straight to watching.

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `BSKY_HANDLE` | Your Bluesky handle | Required |
| `BSKY_PASSWORD` | App password (not your main password) | Required |
| `DOWNLOAD_DIR` | Directory to save downloaded media | `./downloaded_files` |
| `CACHE_FILE` | File tracking what's already downloaded | `./downloaded_cache.txt` |
| `DOWNLOAD_LIMIT` | Max files to download per run | `100` |
| `POLL_INTERVAL` | Seconds between checks in watch mode | `30` |
| `WATCH_ONLY` | Skip initial download, only watch for new likes | `false` |

You can also use the `--watch` flag instead of setting `WATCH_ONLY=true`.

### App Passwords

For Bluesky, use an [app password](https://bsky.app/settings/app-passwords) instead of your account password. This limits access and can be revoked independently.

## How It Works

1. Authenticates with the Bluesky API
2. Fetches your liked posts in batches
3. Extracts media URLs from posts with images or videos
4. Checks each URL against the local cache
5. Downloads new media, skipping already-downloaded files
6. Updates the cache after each successful download

Files are named using a SHA256 hash of the source URL, which prevents duplicates even if the same image appears in multiple posts.

## Roadmap

Support for additional platforms is planned. Each platform has different API access, rate limits, and authentication requirements.

### Planned

- **DeviantArt** - Favorites/collections download
- **Twitter/X** - Liked tweets media (API access dependent)
- **Instagram** - Saved posts (requires authentication workarounds)
- **Reddit** - Saved/upvoted posts from image subreddits
- **Tumblr** - Liked posts media
- **Pixiv** - Bookmarked illustrations
- **Pinterest** - Saved pins
- **ArtStation** - Liked artwork

### Under Consideration

- Graphical TUI
- Advanced caching tools
- GUI version, possibly using native UI components

Platform priority will depend on API availability and community interest. Some platforms may require browser-based authentication or other workarounds due to API restrictions.

## License

MIT
