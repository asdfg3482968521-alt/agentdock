//go:build windows

package envstore

import (
	"os"
)

func platformSystemKeys() []string {
	return []string{
		"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA",
		"SYSTEMROOT", "COMSPEC", "PATHEXT", "TEMP", "TMP", "NODE_PATH",
	}
}

func completePlatformEnv(env map[string]string) {
	temporary := env["TEMP"]
	if temporary == "" {
		temporary = env["TMP"]
	}
	if temporary == "" {
		temporary = os.TempDir()
	}
	if temporary != "" {
		if env["TEMP"] == "" {
			env["TEMP"] = temporary
		}
		if env["TMP"] == "" {
			env["TMP"] = temporary
		}
	}
}
