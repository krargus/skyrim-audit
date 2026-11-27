# skyrim-audit

Offline load-order auditor for **Skyrim SE/AE** on **Mod Organizer 2**. Windows exe, no install, no account.

## Download

Grab the zip from the [Releases](../../releases) page, unzip it anywhere and run `skyrim-audit.exe`.

Windows may warn that the exe is unsigned: choose **More info -> Run anyway**.

## Verify your download

[`CHECKSUMS.txt`](../CHECKSUMS.txt) gives the version, the commit the zip was built from, and the
SHA-256 of `skyrim-audit-share.zip`. Check it before running the exe:

```
certutil -hashfile skyrim-audit-share.zip SHA256          (Command Prompt)
Get-FileHash skyrim-audit-share.zip -Algorithm SHA256      (PowerShell)
```

The hash has to match the one in `CHECKSUMS.txt`. If it does not, download the zip again; if it
still does not, do not run it. The release page also shows each asset's SHA-256, so if a newer
build has replaced the zip, compare against that value instead.

## First run

1.
Double-click `skyrim-audit.exe`.
2. Pick your Mod Organizer 2 folder - the one holding `ModOrganizer.ini`.
3. The audit opens at http://127.0.0.1:8721.

Closing the console window stops it.

## What it checks

- plugin masters, naming the mod that ships a disabled one
- chains of plugins that depend on a broken master
- ESL / light-plugin record limits and the plugin ceilings
- game version and loose-file overlaps between mods
- the newest save's plugin list against the current load order

It can uncheck only plugins that nothing installed can satisfy (backing up
`plugins.txt` first), quarantine a mod with a one-click restore, and write a self-contained HTML report. Nexus artwork, search and requirement checks are the only network calls, cached for a week.

## Terminal use

```
skyrim-audit.exe -v                 full text report
skyrim-audit.exe -fix               uncheck the plugins that cannot load
skyrim-audit.exe -report out.html   write the HTML report
skyrim-audit.exe -json              machine-readable findings (exit 1 on errors)
```

Always close Mod Organizer 2 before letting it change `plugins.txt`: MO2 rewrites the profile on exit.

## How this was built

Written with an AI coding assistant, directed and reviewed by a human. That says nothing about how
correct it is: the full Go source is in this repository, `go build` reproduces the exe, and
`go test ./...` covers the audit rules, so the behaviour can be checked rather than taken on trust.
