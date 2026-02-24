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

2. Copy the example config file and configure:
```bash
cp config.example.yaml config.yaml
```

3. Edit `config.yaml` with your Bluesky credentials:
```yaml
bsky:
  handle: your-handle.bsky.social
  password: your-app-password

download:
  dir: ./downloaded_files
  cache_file: ./downloaded_cache.txt
  limit: 100

poll_interval_minutes: 30

# Leave empty to disable
ntfy_topic:
health_port:
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

1. **Initial download** - Fetches your existing likes and downloads media up to `download.limit`
2. **Watch mode** - Polls for new likes every `poll_interval_minutes` minutes and downloads new media

Use `--watch` to skip phase 1 and go straight to watching.

## Configuration

| Key | Description | Default |
|-----|-------------|---------|
| `bsky.handle` | Your Bluesky handle | Required |
| `bsky.password` | App password (not your main password) | Required |
| `download.dir` | Directory to save downloaded media | `./downloaded_files` |
| `download.cache_file` | File tracking what's already downloaded | `./downloaded_cache.txt` |
| `download.limit` | Max files to download per run | `100` |
| `poll_interval_minutes` | Minutes between checks in watch mode | `30` |
| `ntfy_topic` | ntfy.sh topic for push notifications (disabled if empty) | - |
| `health_port` | Port for health endpoint (disabled if empty) | - |

You can use the `--watch` flag to skip the initial download and go straight to watching.

### Push Notifications

Set `ntfy_topic` in your `config.yaml` to receive push notifications when new media is downloaded.

1. Install the [ntfy app](https://ntfy.sh) on your phone (iOS/Android)
2. Subscribe to your topic (e.g., `your-topic-name`)
3. Set `ntfy_topic: your-topic-name` in `config.yaml`

You'll receive a notification whenever new liked media is downloaded in watch mode.

### Uptime Kuma

Set `health_port: 8080` in your `config.yaml` to enable a health endpoint for [Uptime Kuma](https://github.com/louislam/uptime-kuma):

1. Add a new monitor in Uptime Kuma
2. Monitor type: HTTP(s)
3. URL: `http://your-server:8080/health`

This lets you monitor if LikeLocker is running and get alerts if it stops.

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
