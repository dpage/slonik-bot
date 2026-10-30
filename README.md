# slonik-bot

slonik-bot is a command line tool that drives Chrome into a Google Meet call, reads the live captions, and asks a language model to summarise the meeting once it ends. It writes a transcript, a summary and a list of action items to a folder on your machine.

## What it does, and its limits

The bot joins the call as a signed-in Google Workspace account, turns on Meet's captions and scrapes the caption text from the page. It then writes the transcript to disk as it goes, so a crash part-way through still leaves something to summarise.

- It works from captions only; it does not hear or record audio or video. If nobody is talking, or Meet cannot caption what is said, there is nothing to transcribe.
- Captions are in English unless you change `caption_language`.
- Meet's page markup is not a stable interface and can change without notice. When it does, the bot may fail to find the captions until it is updated.

## Privacy

The bot announces itself: it joins under the account's display name and posts `chat_notice` to the meeting chat, so participants know a transcript is being made. Tell people before you invite it, and follow your organisation's rules on recording meetings.

Everything is kept locally. The only data sent elsewhere is the transcript text, which goes to the summary model you configure. Output folders are created with mode 0700 and the files in them with mode 0600, so other users on the same machine cannot read them.

## Requirements

- Go (to build from source), or Docker.
- Google Chrome or Chromium. On Linux, set `chrome_path` if it is not found automatically.
- Either an API key for Anthropic or OpenAI, or an OpenAI-compatible server (Ollama, for example) set up through `llm.base_url`. Such servers may need no key at all.

The API key is read from the environment variable named by `llm.api_key_env` (by default `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, depending on the provider). If that variable is empty, the bot reads `~/.anthropic-api-key` or `~/.openai-api-key` instead, trimming any whitespace. It warns if that file can be read by other users, so run `chmod 600` on it. The key is never stored in the configuration file.

## Setting up the bot's Google account

Create a dedicated Workspace account for the bot rather than using a personal one.

1. In the Workspace admin console, give the account a clear display name, such as 'Slonik Bot (transcribing)'. Meet shows the account's name when someone is signed in; `bot_name` is only used if Meet offers a name field.
2. Make sure the account can join your meetings without knocking, which means it is in the same organisation as the meeting or has been invited. If it is left in the lobby, the bot waits five minutes and then gives up.
3. Run `slonik-bot login` once (see below) to sign the account in.

## Install

```sh
go install github.com/dpage/slonik-bot/cmd/slonik-bot@latest
```

or, from a checkout:

```sh
make build
```

which writes `bin/slonik-bot`.

## Usage

### Sign in

```sh
slonik-bot login
```

This opens a Chrome window using the bot's own profile (`profile_dir`). Sign in as the bot's account, complete any verification, and close the window. The profile keeps the session, so you only need to do this again if Google signs the account out. `login` always shows the window and ignores `--headless`.

### Join a meeting

```sh
slonik-bot join abc-defg-hij
```

You can give either the meeting code or the full meeting URL. The bot joins, transcribes until the meeting ends or it is alone for `alone_timeout`, or `max_duration` passes, and then writes the notes.

To stop it yourself, press Ctrl-C once and wait. The bot leaves the call and then writes the summary; a second signal during that can leave Chrome running.

Flags for `join`:

| Flag | Meaning |
| --- | --- |
| `--config` | Configuration file (default `~/.config/slonik-bot/config.yaml`) |
| `--headless` | Run Chrome without a window (off by default) |
| `--llm-model` | Summary model, overriding the configuration |
| `--llm-provider` | `anthropic` or `openai`, overriding the configuration |
| `--max-duration` | Leave after this long, overriding `max_duration` |
| `--no-summary` | Save the transcript without asking a model for a summary |
| `--out` | Directory to hold the meeting folders, overriding `output_dir` |
| `--verbose` | Log debugging detail |

### Summarise a saved transcript

```sh
slonik-bot summarise ~/slonik-bot-notes/2026-10-30-abc-defg-hij/transcript.jsonl
```

This re-runs the summary for a saved `transcript.jsonl`, for example after changing the model or when the first attempt failed. If a summary fails at the end of a meeting, the bot prints the exact `summarise` command to run, so you can copy it. `summarise` accepts `--config`, `--llm-model`, `--llm-provider`, `--out` (default: the transcript's own directory) and `--verbose`.

## Configuration

Save this as `~/.config/slonik-bot/config.yaml`, or under `$XDG_CONFIG_HOME/slonik-bot/`. Every key is optional; the values shown are the defaults.

```yaml
# Display name hint. The account's name is what Meet shows when signed in, and
# that is set in the Workspace admin console; this is used only when Meet
# offers a name field.
bot_name: "Slonik Bot (transcribing)"
chat_notice: "Slonik Bot is transcribing this call. The summary, action items and transcript will be shared with participants."
caption_language: "English"
profile_dir: "~/.config/slonik-bot/chrome-profile"
chrome_path: ""            # empty: chromedp's own discovery
output_dir: "~/slonik-bot-notes"
max_duration: 4h
alone_timeout: 60s
llm:
  provider: anthropic      # anthropic | openai
  model: ""                # default per provider: claude-opus-5-5 for anthropic; required for openai
  base_url: ""             # openai only, e.g. http://localhost:11434/v1
  api_key_env: ""          # default ANTHROPIC_API_KEY / OPENAI_API_KEY; if unset, the key is read from ~/.anthropic-api-key / ~/.openai-api-key; it is never stored in this file
  max_output_tokens: 16000
```

## Output

Each meeting gets its own folder under `output_dir`, named by the date it started and the meeting code:

```text
~/slonik-bot-notes/
  2026-10-30-abc-defg-hij/
    transcript.jsonl   raw captions, one JSON object per line, written as the meeting runs
    meeting.json       meeting details (code, start time and so on)
    transcript.md      readable transcript
    summary.md         summary, decisions and action items
```

`transcript.jsonl` and `meeting.json` are what `summarise` reads. The folders are mode 0700 and the files mode 0600.

## Docker

The image bundles Chromium and a virtual display (Xvfb), so it runs on Apple Silicon as well as x86 hosts. Build it with:

```sh
docker build -t slonik-bot:dev .
```

Inside the container, the configuration lives in `/config` and the notes in `/notes`. Use a `config.yaml` that points at them:

```yaml
chrome_path: /usr/bin/chromium
output_dir: /notes
```

Save it as `~/.config/slonik-bot-docker/slonik-bot/config.yaml` (the container sets `XDG_CONFIG_HOME=/config`, so the file is looked for at `/config/slonik-bot/config.yaml`), then run:

```sh
docker run -it --rm \
  -v ~/.config/slonik-bot-docker:/config \
  -v ~/slonik-bot-notes:/notes \
  -e ANTHROPIC_API_KEY \
  slonik-bot:dev join abc-defg-hij
```

The container's home directory is `/home/slonik`, so pass the key as an environment variable as above. If you prefer a key file, mount it read-only:

```sh
-v ~/.anthropic-api-key:/home/slonik/.anthropic-api-key:ro
```

To stop a run, use `docker stop -t 60 <container>` (or press Ctrl-C once in an interactive session and wait), so the bot has time to leave the call and write the summary before Docker kills it.

### Signing in inside the container

A Chrome profile signed in on macOS cannot be reused inside the Linux container, so sign in inside the container. On a Linux host with an X server, share the display:

```sh
xhost +local:
docker run -it --rm \
  -e DISPLAY -v /tmp/.X11-unix:/tmp/.X11-unix \
  -v ~/.config/slonik-bot-docker:/config \
  slonik-bot:dev login
```

The sign-in is saved in the mounted `/config` directory (the profile defaults to `/config/slonik-bot/chrome-profile`), so later `join` runs reuse it. Run `xhost -local:` afterwards to close the display back up.

## Troubleshooting

- **Not signed in.** The bot reports that the profile is not signed in, or Meet asks for a name or sign-in. Run `slonik-bot login` again (or the Docker login recipe above) and make sure the profile directory is the one `join` uses.
- **Not admitted.** The bot sits at the lobby and gives up. The account must be in the meeting's organisation or invited, so it can join without anyone letting it in.
- **Captions not found.** Meet's layout has probably changed. Run with `--verbose` and without `--headless` to see what the bot is doing, and open an issue with the log output (check it for names and meeting details first).
- **No summary.** If the model call fails, the transcript is still saved; fix the key or model and run the `summarise` command the bot printed.
