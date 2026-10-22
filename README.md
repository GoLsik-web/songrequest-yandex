# songrequest for Yandex Music — song requests for Twitch

**Your viewers request songs right from the stream, and the music plays in your own Yandex Music.**

> **Is this the right version for you?** This build works with **Yandex Music**,
> which is available mainly in Russia and the CIS, and its panel and setup wizard
> are **in Russian only**. If you stream in English or use Spotify, get the
> Spotify version instead: **https://github.com/GoLsik-web/songrequest** — same
> features, English interface.

[**⬇ Download the latest version**](../../releases/latest) · Windows 10/11 · free

**[Русская версия](README.ru.md)**

---

## What it does

A viewer redeems channel points or sends a donation and types a song name.
The app finds the track and adds it to your Yandex Music queue right after the
current song. When the requests run out, your own music picks up where it
stopped. A "now playing" card shows up on your stream.

## What you need

- Windows 10 or 11;
- **Yandex Plus** — without it, Yandex Music plays only 30-second previews;
- the Yandex Music app on your PC (or music.yandex.ru in a browser);
- a Twitch channel (Affiliate or Partner for channel points requests);
- OBS, if you want the on-screen card.

## Getting started

1. Download the archive from the [releases page](../../releases/latest) and unzip it.
2. Run `songrequest-yandex.exe` — nothing to install.
3. A setup wizard (in Russian) walks you through signing in to Yandex, Twitch,
   the channel points reward, donations and the OBS card.

## Features

- Requests for channel points or donations (DonationAlerts, DonatePay, DonateX).
- Points are refunded automatically if a track isn't found or breaks your rules.
- Your own music resumes after the requests.
- "Now playing" card for OBS with many styles.
- Falls back to YouTube if a track isn't on Yandex Music (optional).
- Chat commands for you and your mods.

## If something doesn't work

Every error in the panel has a short code (like `YM-03`) and a hint. The
"Сохранить лог и историю" (save log and history) button in the settings packs
everything needed for troubleshooting into one archive; sign-in data and keys
never go into it.

---

## For developers

```
go run .                      # app window with the panel inside
go run . -в-браузере          # no window, panel in your browser
go run . -без-браузера        # open neither window nor browser
go test ./...                 # tests
.\build.ps1 -Version X.Y.Z    # builds dist\songrequest-yandex.exe and the zip
```

Written in Go. The panel is a web page embedded in the `.exe` with its fonts,
so it works offline. Panel: `http://127.0.0.1:8978`, OBS card:
`http://127.0.0.1:8978/widget`. Settings, database and logs are in
`%APPDATA%\songrequest-yandex`. It does not overlap with the Spotify version:
separate folder, separate port.

Yandex Music has no official API, so the app talks to it the way its own
clients do (`internal/yandex`): `api.music.yandex.net` for search, tracks,
playlists and the account; **Ynison**, the shared playback-queue connection,
to insert a request right after the current track. If Ynison is unavailable,
requests go to a "Заказы со стрима" playlist and viewers keep their points.
The current track is also read from the Windows media panel
(`internal/smtc`, `internal/localapp`). Error codes live in `internal/errs`.
