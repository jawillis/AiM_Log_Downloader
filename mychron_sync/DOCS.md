# MyChron Sync

Downloads new sessions from your AiM logger whenever it joins your network, and
saves them as `.xrk` files you can open in Race Studio 3 or any other analysis tool.

## Before you start

1. Put the logger on your router's Wi-Fi ("Existing network" mode in Race Studio 3).
2. Give it a fixed address: a DHCP reservation on the router is best.
3. Make sure Race Studio is **closed** on every computer. The logger only
   accepts one connection at a time.

## Options

| Option | Default | What it does |
| --- | --- | --- |
| `logger_host` | *(empty)* | The logger's IP address. Required. |
| `poll_interval_seconds` | 15 | How often to check whether the logger is on the network. |
| `resync_interval_minutes` | 5 | While the logger stays on the network, how often to look for new sessions. |
| `settle_seconds` | 5 | How long to wait after the logger appears before connecting. |
| `probe` | `auto` | How to check the logger is there. `udp` is gentlest on the logger. `tcp` connects and disconnects. `auto` tries UDP, then TCP. |
| `output_dir` | `/share/datalogger` | Where sessions are saved. |
| `keep_raw` | off | Also keep the compressed download in a `raw` folder. Sessions that cannot be decompressed are always kept as received. |
| `log_level` | `info` | Use `debug` when troubleshooting. |

## Where your data goes

Sessions are saved to `/share/datalogger` (change it with `output_dir`). With
the Samba add-on installed, that is `\\homeassistant.local\share\datalogger`.

Files are named by session number and track, for example `a_0775_RCQMA.xrk`.
The logger's own dates are unreliable (many sessions are stamped 29/11/2015
because its clock wasn't set), so the date is not part of the file name. The
session page shows the date when it looks valid.

The add-on never deletes anything from the logger or from the share. A small
record of what has been downloaded is kept in `.mychron-sync/manifest.json`
inside the output folder, so it travels with your data when you back it up.

The first time it runs, it downloads everything on the logger, newest first.

## The sessions page

Each saved session shows its track, number of laps, best lap, length, and when
it was recorded.

- **Best lap** is the time the logger itself recorded, and is blank ("None")
  when the logger didn't time any laps. A *Track best* tag marks your fastest
  saved lap at each track.
- **Only sessions with a lap time** are shown by default, which gets rid of
  the false starts and the sessions where the logger didn't time any laps.
  The line above the table always says how many are hidden, and *Show all*
  brings them back. You can also hide short sessions (under 1, 2 or 5
  minutes), filter by track, and sort by fastest lap or length. The page
  remembers your choices.
- If the logger left the track name blank, the page uses the name of other
  sessions recorded within 1 km and says it was guessed from position. The
  saved file name and the logger's own data are not changed.

Nothing is hidden or deleted on disk: filters only change what the page shows.

Details for every session (driver, kart number, position, and every other
column the logger reports) are kept in `.mychron-sync/manifest.json`. Sessions
saved by an earlier version are filled in the next time the add-on lists the
logger, without downloading anything again.

## Pausing

Use **Pause syncing** on the add-on page before you connect Race Studio. While
paused, the add-on does not contact the logger at all.

## In Home Assistant

Two sensors are created:

- `sensor.mychron_sync_status`: `offline`, `waiting`, `syncing`, `idle`, `error` or `paused`
- `sensor.mychron_sync_sessions`: how many sessions have been saved

An event, `mychron_sync_new_sessions`, fires after each sync that saves
something. Its data has `count`, `sessions` and `tracks`, and when any of the
new sessions has a lap time, also `best_lap` (for example `1:49.549`),
`best_lap_ms`, `best_lap_track` and `best_lap_session`. To get a phone
notification:

```yaml
triggers:
  - trigger: event
    event_type: mychron_sync_new_sessions
actions:
  - action: notify.mobile_app_YOUR_PHONE
    data:
      title: Logger sync
      message: >
        Saved {{ trigger.event.data.count }} session(s)
        {% if trigger.event.data.tracks %}from {{ trigger.event.data.tracks | join(', ') }}{% endif %}.
        {% if trigger.event.data.best_lap is defined %}Best lap {{ trigger.event.data.best_lap }}{% endif %}
```

These sensors are created through Home Assistant's API, so they disappear on a
Home Assistant restart until the add-on next publishes a change.

## Troubleshooting

- **Stays on "Waiting for the logger" while the logger is on.** Check the
  address, then try `probe: tcp`.
- **"Sync failed" with "logger stopped responding".** The logger's Wi-Fi
  chip sometimes stops answering. Power-cycle the logger. The add-on retries
  with increasing delays and keeps whatever it already saved.
- **Race Studio can't connect.** Pause syncing first.
