package buildinfo

import "testing"

func TestInstallScript(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	for version, want := range map[string]string{
		"v1.2.3":             Repository + "/releases/download/v1.2.3/install.sh",
		"v1.3.0-rc.1":        Repository + "/releases/download/v1.3.0-rc.1/install.sh",
		"dev":                Repository + "/releases/latest/download/install.sh",
		"v1.2.3-4-gdeadbee":  Repository + "/releases/latest/download/install.sh",
		"v0.1.1-SNAPSHOT-ab": Repository + "/releases/latest/download/install.sh",
		"v1.2.3-dirty":       Repository + "/releases/latest/download/install.sh",
		"v1.2.3-dev":         Repository + "/releases/latest/download/install.sh",
		"v1.3.0-beta.2":      Repository + "/releases/download/v1.3.0-beta.2/install.sh",
	} {
		Version = version
		if got := InstallScript(); got != want {
			t.Errorf("InstallScript() for %s = %s, want %s", version, got, want)
		}
	}
}
