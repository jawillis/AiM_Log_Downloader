# MyChron Sync

A Home Assistant add-on that downloads new sessions from an AiM MyChron or
Solo 2 DL datalogger as soon as it joins your network, for example when you roll
back into the pits and the logger reconnects to the trailer's Wi-Fi.

- Saves each session as an `.xrk` file on a share you can reach from any computer
- Shows best lap, length and track for every session, hides the false starts, and marks your best lap at each track
- Detects the logger coming and going, retries when the logger's Wi-Fi misbehaves
- Web page in the Home Assistant sidebar, protected by your Home Assistant login
- Publishes sensors and an event so you can get a phone notification

**Status: first milestone.** It is tested against a simulated logger, not yet
against real hardware. See *What is and isn't verified* below.

## Install

You need internet access on the Pi while installing, because the add-on is
built on the device.

**Local install (no GitHub needed)**

1. Install the *Samba share* add-on in Home Assistant and open its `addons` share.
2. Copy the `mychron_sync` folder into it.
3. Settings, Add-ons, Add-on Store. *MyChron Sync* appears under *Local add-ons*.
   If it doesn't, open the menu (top right) and choose *Check for updates*, then reload the page.
4. Install it, set `logger_host` on the Configuration tab, then start it.

**Updating a local install.** Replace the `mychron_sync` folder on the share
(Finder can fail on folder copies; see below), then open the add-on's page, use
the three-dot menu, and choose **Rebuild**. *Check for updates* does not reliably
notice changes to local add-ons; Rebuild makes Home Assistant re-read
`config.yaml`. When it sees the new version number, the **Update** button
becomes active.

If macOS Finder fails with error -8062 when copying the folder, copy with
Terminal instead, with the share mounted:
`rsync -rt --delete --exclude='.DS_Store' mychron_sync/ /Volumes/addons/mychron_sync/`

**From a repository.** Put this repo on GitHub, change the URLs in
`repository.yaml` and `mychron_sync/config.yaml`, and add the repository URL in
the Add-on Store.

## Try it on its own first

The binary has two diagnostic commands that talk to the logger directly, so you
can check connectivity before involving Home Assistant:

```
cd mychron_sync
go run ./cmd/mychron-sync list -host 192.168.1.50
go run ./cmd/mychron-sync get  -host 192.168.1.50 a_0775.xrz
```

To run the whole app locally with the status page on http://127.0.0.1:8099:

```
go run ./cmd/mychron-sync serve -host 192.168.1.50 -out ./sessions -allow-any-client
```

Go 1.22 or newer is the only requirement; there are no third-party dependencies.
`go test ./...` runs the test suite.

## What is and isn't verified

Verified here, with automated tests:

- Lap times are milliseconds, confirmed against a real logger (91234 is 1:31.234)

- Frame encoding matches the byte sequences in the protocol spec
- Multi-chunk downloads reassemble correctly; bad checksums are rejected
- A logger that goes silent mid-transfer is detected, and nothing partial is saved
- First run downloads everything, newest first; later runs download only what's new
- Restarts don't re-download; the same session name with a different size is kept as a new version
- Only Home Assistant's ingress gateway can reach the web page

**Not yet verified on real hardware or a real Home Assistant:**

- The add-on build and ingress setup (written from the add-on documentation, not run)
- Whether a Solo 2 DL answers the UDP presence check without an open TCP session
  (`probe: auto` falls back to TCP if it doesn't)
- Sessions from older `.hrz` files are inflated the same way as `.xrz`; this
  worked with the reference client but is untested here
- The logger's behaviour around the settle delay and its Wi-Fi power mode

## Credits and license

The protocol was reverse engineered by TheAngryRaven in
[mychron-wifi-spec](https://github.com/TheAngryRaven/mychron-wifi-spec). The
protocol code here is a Go port of that project's Python client, so this project
is released under the same license, GPL-3.0-or-later (see `LICENSE`).

AiM, MyChron and Race Studio are trademarks of their respective owners. This
project is not affiliated with or endorsed by AiM Tech.
