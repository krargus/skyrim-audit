package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// pickInstanceFolder asks for the MO2 folder in a native Windows folder dialog.
// A bare launch uses it when no instance is remembered or found, so a friend who
// was handed the exe can point it at their own instance without a command line.
func pickInstanceFolder() (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("no folder dialog on this system")
	}
	script := `Add-Type -AssemblyName System.Windows.Forms;` +
		`$d = New-Object System.Windows.Forms.FolderBrowserDialog;` +
		`$d.Description = 'Select your Mod Organizer 2 folder (the one holding ModOrganizer.ini)';` +
		`$d.ShowNewFolderButton = $false;` +
		`if ($d.ShowDialog() -eq 'OK') { $d.SelectedPath }`
	out, err := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script).Output()
	if err != nil {
		return "", fmt.Errorf("folder dialog failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
