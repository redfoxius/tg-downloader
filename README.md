# tg-downloader (`tgarchive`)

A local tool for building an offline archive of Telegram channels (workshops, courses): posts, videos/files/photos and the comment thread under every post. The result is a static HTML site that opens from `file://` with no internet and no external CDNs.

It works as a **user client** (not a bot) over MTProto ([`gotd/td`](https://github.com/gotd/td)). It is meant for personal use of content you have legal access to.

## How it works

```
browser ──> tgarchive serve (127.0.0.1) ──> Telegram
                 │
                 ├─ data/archive.db            SQLite: posts, comments, authors, media queue
                 └─ data/<channel>/media/…     downloaded files
                    data/<channel>/site/…      generated offline site
```

1. **Log in** in the web UI (QR code or phone + code).
2. Pick a channel, set what to archive (date range, comments, file types) and press **Archive**.
3. One job does everything: posts → comments → media files → offline site (the last step can be switched off). Run it again any time: it is incremental, finished files are skipped and interrupted ones continue.
4. **Open site** on the channel page opens the result (it is also a plain folder you can open from disk).

## Requirements

- Go 1.25+ (no CGO: SQLite is pure Go, `modernc.org/sqlite`).
- Your own `api_id` and `api_hash`.

### Getting `api_id` / `api_hash`

MTProto itself requires them (QR login only replaces the phone number and code, not these).

1. Open <https://my.telegram.org> and sign in with your phone number.
2. **API development tools** → create an application (name and description are arbitrary).
3. Copy `App api_id` and `App api_hash`.

Do not use someone else's public `api_id`: FLOOD_WAIT limits are shared with everyone on it, and the risk of restrictions on your account is higher.

## Running

```sh
go build -o tgarchive ./cmd/tgarchive
./tgarchive serve                 # QR login, http://127.0.0.1:8181/
./tgarchive serve --login phone   # login with phone number and code
```

Open `http://127.0.0.1:8181/`. If `api_id`/`api_hash` are not set anywhere yet, the first screen asks for them and saves them to the config.

| Flag | Default | Description |
|------|---------|-------------|
| `--login qr\|phone` | `qr` | Login method. |
| `--config <path>` | see below | Config file. |
| `--addr host:port` | `127.0.0.1:8181` | Web UI address. |

Ctrl+C stops the server; a download in progress is left resumable.

### Login

- **QR**: in Telegram on your phone go to *Settings → Devices → Link Desktop Device* and scan the code on the page. If the account has 2FA, a password field appears.
- **Phone**: enter the number, then the code from Telegram (and the 2FA password if needed).
- **The session is stored** in `session.json` next to the config and reused: if it is still valid, login is skipped. If it has expired, the login screen is shown.
- **Log out** (main page) ends the session on Telegram's side, deletes `session.json` and returns to the login screen so another account can sign in. The archive (`archive.db`, media) is not touched; for a different account it is better to use a different `data_dir`.

## Configuration

A YAML file; the default path is `os.UserConfigDir()/tgarchive/config.yaml` (Linux: `~/.config/tgarchive/`, macOS: `~/Library/Application Support/tgarchive/`). Environment variables override the file.

```yaml
api_id: 123456            # or env TG_API_ID
api_hash: "0123abcd…"     # or env TG_API_HASH
session_path: ""          # defaults to session.json next to the config
data_dir: data            # archive and media (relative to the working directory)
addr: 127.0.0.1:8181
rate_limit: 5             # Telegram API requests per second
```

## Channel settings

Set on the channel page, saved automatically when you press **Archive**.

| Setting | Effect |
|---------|--------|
| Last N days / Since date | Depth: only newer posts are synced (the later of the two limits wins). 0 / empty = everything. |
| Save comments | Whether to fetch comment threads. |
| Download videos / documents / photos | Which file types to download. |
| Document extensions | Allowed document extensions (`pdf,zip`), empty = any. Videos and photos are not filtered by extension. |
| Max file size, MB | Size limit, 0 = unlimited. |
| Parallel downloads | Number of simultaneous downloads (1–16). |
| Build the site when archiving finishes | Generate the offline site as the last step of the job. |
| Show comments / videos / documents / photos (site) | What the generated site shows, independent of what was downloaded. The site covers the same date range as the sync. |

Filters are applied during sync and again right before downloading. Widening the depth triggers a rescan back to the new cutoff; narrowing it does not.

## What lives where

```
data/archive.db                           SQLite (WAL), one database for all channels
data/<username|channel-<id>>/media/       post-<id>-<mediaId>-<name>, comment-…
data/<…>/site/index.html                  offline site (+ posts/<id>.html, style.css)
```

The site references `../../media/…`, so keep the channel folder together: you can copy it as a whole (USB drive, another computer) and open `site/index.html` with a double click.

## Reliability and limits

- **FLOOD_WAIT** is handled by the `floodwait` middleware from `gotd/contrib`, plus a rate limit on API calls. File chunk downloads (`upload.getFile`) are excluded from the rate limit, otherwise they would be capped at ~5 MB/s across all files.
- **Resuming:** files are written to `<name>.part`; once complete, they are `fsync`ed and renamed. An interrupted or aborted job leaves only a `.part` that continues from a 1 MB boundary next time. Already downloaded files are skipped (by presence and size). Files that failed are retried on the next run.
- A stale `file_reference` is refreshed before each file and, if needed, in the middle of a long download.
- One job runs at a time; it can be stopped with **Cancel**.

## noforwards

Channels with content protection (`noforwards`) are not archived until *"I have legal access to this content"* is ticked in the form.

## Security

- The web UI listens on **loopback only** and rejects requests with a different `Host`/`Origin` (protection against DNS rebinding and CSRF). Do not expose it to the network.
- `session.json` gives full access to the account and `config.yaml` contains `api_hash`. Both are created with `0600` permissions; do not commit them (`.gitignore` covers them along with `data/`).
- Post and comment text is HTML-escaped; links are limited to `http(s)`, `mailto` and `tg`.

## Known limitations

- Edits and deletions of posts/comments after a sync are not tracked (only new comments are added; a changed comment count triggers fetching the new ones).
- Polls, stickers and other posts without text or files are skipped.
- Comments written on behalf of a channel are stored without an author.
- Telegram CDN redirects for files are not supported (rare; the file is marked as failed).
- The export shows only downloaded files; a file still in the queue does not appear on the site.
- Dates on the site use the time zone of the machine that ran the export.

## Development

```sh
go vet ./... && go test ./...
```

```
cmd/tgarchive/        entry point (serve), dependency wiring
internal/config/      YAML + env
internal/telegram/    gotd client: login, dialogs, posts, comments, files
internal/storage/     SQLite: migrations (embedded), repository, channel settings
internal/syncer/      posts and comments: incremental sync, depth, filters, noforwards
internal/downloader/  worker pool, .part files, resume, progress
internal/export/      HTML site, entities → HTML, comment tree
internal/web/         local UI and job runner
```

Tests cover storage, syncer, downloader (with a fake Telegram), export and the web handlers. Real Telegram interaction (login, `getHistory`/`getReplies`, `upload.getFile`) can only be verified with a live run.
