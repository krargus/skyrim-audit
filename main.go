package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	var (
		instance   = flag.String("instance", "", "MO2 instance folder, or a profile folder holding plugins.txt (default: remembered, else auto-detected)")
		profile    = flag.String("profile", "", "profile name (default: the profile MO2 has open)")
		data       = flag.String("data", "", "game Data folder, when ModOrganizer.ini has no game_path")
		eslLimit   = flag.Int("esl-limit", 0, "records a light plugin may hold; 0 = read it from the installed game version")
		serve      = flag.String("serve", "", "start the web UI on this address, e.g. 127.0.0.1:8720 (default when launched with no flags)")
		open       = flag.Bool("open", true, "open the UI in the default browser")
		nexusGame  = flag.String("nexus-game", "skyrimspecialedition", "Nexus game domain used for mod search")
		noNexus    = flag.Bool("no-nexus", false, "disable every Nexus lookup: mod search, artwork and requirement checks (stays offline)")
		asJSON     = flag.Bool("json", false, "print findings as JSON")
		verbose    = flag.Bool("v", false, "include informational findings")
		fix        = flag.Bool("fix", false, "uncheck the plugins that cannot load and cannot be repaired, then re-audit (plugins.txt is backed up); a plugin whose master is only unchecked is left alone")
		reportPath = flag.String("report", "", "also write a self-contained HTML report to this path")
	)

	flag.Usage = func() {
		fmt.Fprint(os.Stderr, "skyrim-audit - offline load-order audit for Skyrim SE/AE on Mod Organizer 2\n\n")
		fmt.Fprint(os.Stderr, "run it with no arguments to start the local web UI on the instance it remembers.\n\n")
		fmt.Fprint(os.Stderr, "usage: skyrim-audit [-instance <MO2 folder>] [-profile <name>] [-data <Data folder>] [-json] [-v] [-fix] [-report out.html]\n")
		fmt.Fprint(os.Stderr, "       skyrim-audit [-instance <MO2 folder>] -serve 127.0.0.1:8720\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	// A double-click passes no flags: that launch shows the UI, and on failure it
	// keeps the console on screen instead of closing before the message is read.
	launchedBare := flag.NFlag() == 0
	stop := fatal
	if launchedBare {
		stop = fatalPaused
	}

	dir := strings.TrimSpace(*instance)
	if dir == "" {
		found, err := findInstance()
		if err != nil && !launchedBare {
			stop(err)
		}
		// A shared exe lands on a machine with an instance nobody can guess at,
		// so ask for the folder instead of failing with a command-line hint.
		var pickErr error
		for tries := 0; found == "" && tries < 3; tries++ {
			picked, err := pickInstanceFolder()
			if err != nil {
				pickErr = err
				break
			}
			if picked == "" {
				break // the dialog was cancelled
			}
			if _, err := OpenInstance(picked, *profile); err != nil {
				fmt.Fprintf(os.Stderr, "that folder cannot be used: %v\n", err)
				continue
			}
			found, *instance = picked, picked
		}
		if found == "" {
			if pickErr != nil {
				stop(pickErr)
			}
			stop(fmt.Errorf("no Mod Organizer 2 folder was chosen - run " +
				"skyrim-audit.exe -instance \"C:\\path\\to\\ModOrganizer\" to set it"))
		}
		dir = found
	}

	inst, err := OpenInstance(dir, *profile)
	if err != nil {
		stop(err)
	}
	// Only interactive launches (the UI) remember the instance. A one-off CLI run
	// against a throwaway folder must not replace the folder the double-click
	// launch depends on: that leaves the next launch looking for a folder that no
	// longer exists, with nothing on screen to explain it.
	if *instance != "" && (launchedBare || *serve != "") {
		rememberInstance(inst.Dir)
	}
	inst.SetGameData(*data)

	game := DetectGameVersion(inst.GameData)
	limit := *eslLimit
	if limit <= 0 {
		limit = game.EslLimit
	}
	opts := Options{EslRecordLimit: limit, Game: &game}

	if *serve != "" || launchedBare {
		fmt.Printf("instance: %s\nprofile:  %s\n", inst.Dir, inst.Profile)
		srv := NewServer(inst, opts, *nexusGame, !*noNexus)
		if launchedBare {
			err = srv.ServeOnFirstFreePort("127.0.0.1", 8721, *open)
		} else {
			err = srv.ListenAndServe(*serve, *open)
		}
		if err != nil {
			stop(err)
		}
		return
	}

	res, err := Audit(inst, opts)
	if err != nil {
		stop(err)
	}
	// Same rule as the web UI: an override a mod declares as a requirement on
	// Nexus is by design, not something the report should flag.
	nx := NewNexusClient(*nexusGame, *noNexus)
	suppressRequiredOverrides(res, nx)

	if *fix && len(res.BrokenPlugins) > 0 {
		// Only uncheck what cannot be repaired. A plugin whose master is merely
		// unchecked loads as soon as that mod is enabled, and disabling the
		// dependent instead would hide the real fix.
		if len(res.DisableCandidates) == 0 {
			fmt.Fprintln(os.Stderr, "-fix: nothing to disable - these plugins load once the missing or unchecked plugin named in the errors above is provided")
		} else {
			out, err := DisablePlugins(inst, res.DisableCandidates)
			if err != nil {
				stop(err)
			}
			fmt.Fprintf(os.Stderr, "-fix: unchecked %d plugin(s): %s\n", len(out.Disabled), strings.Join(out.Disabled, ", "))
			if len(out.Skipped) > 0 {
				fmt.Fprintf(os.Stderr, "-fix: skipped %d not listed as enabled: %s\n", len(out.Skipped), strings.Join(out.Skipped, ", "))
			}
			// Report the load order as it now stands, so the exit code means something.
			if res, err = Audit(inst, opts); err != nil {
				stop(err)
			}
			suppressRequiredOverrides(res, nx)
		}
	}

	if *reportPath != "" {
		if err := res.WriteHTMLReport(*reportPath); err != nil {
			stop(err)
		}
		fmt.Fprintf(os.Stderr, "report written to %s\n", *reportPath)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fatal(err)
		}
	} else {
		fmt.Print(res.Render(*verbose))
	}

	if res.Counts.Error > 0 {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "skyrim-audit: %v\n", err)
	os.Exit(2)
}

// fatalPaused leaves the error on screen for a double-clicked exe, whose console
// window would otherwise disappear with the message.
func fatalPaused(err error) {
	fmt.Fprintf(os.Stderr, "skyrim-audit: %v\n\npress Enter to close\n", err)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(2)
}
