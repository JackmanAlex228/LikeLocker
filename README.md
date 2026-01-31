# LikeLocker

LikeLocker downloads and archives media (images and videos) from your liked posts on social media platforms. Your likes represent content you found valuable enough to save - this tool helps you actually keep it before it disappears.

Currently supports **Bluesky**. More platforms planned.

## Features

- Downloads images and videos from liked posts
- Incremental downloads with local cache (only fetches new content)
- Configurable download limits per run
- Video support via ffmpeg (handles HLS streams)
- Docker support for easy deployment
- Content-addressed filenames (SHA256 hash) to avoid duplicates

## Requirements

- Go 1.24+ (for building from source)
- ffmpeg (for video downloads)
- A Bluesky account with an app password

Or just Docker.

## Setup

### Option 1: Docker (Recommended)

1. Clone the repository:
```bash
git clone https://github.com/yourusername/LikeLocker.git
cd LikeLocker
```

2. Copy the example environment file and fill in your credentials:
```bash
cp .env.example .env
```

3. Edit `.env` with your Bluesky credentials:
```
BSKY_HANDLE=your-handle.bsky.social
BSKY_PASSWORD=your-app-password
DOWNLOAD_LIMIT=100
```

4. Run with Docker Compose:
```bash
docker compose up --build
```

Downloaded files will appear in `./downloaded_files/`.

### Option 2: Build from Source

1. Clone and build:
```bash
git clone https://github.com/yourusername/LikeLocker.git
cd LikeLocker
go build -o likelocker .
```

2. Set up environment variables (or create a `.env` file):
```bash
export BSKY_HANDLE=your-handle.bsky.social
export BSKY_PASSWORD=your-app-password
export DOWNLOAD_DIR=./downloaded_files
export CACHE_FILE=./downloaded_cache.txt
export DOWNLOAD_LIMIT=100
```

3. Run:
```bash
./likelocker
```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `BSKY_HANDLE` | Your Bluesky handle | Required |
| `BSKY_PASSWORD` | App password (not your main password) | Required |
| `DOWNLOAD_DIR` | Directory to save downloaded media | `./downloaded_files` |
| `CACHE_FILE` | File tracking what's already downloaded | `./downloaded_cache.txt` |
| `DOWNLOAD_LIMIT` | Max files to download per run | `100` |

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
