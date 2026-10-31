package template

import (
	"testing"

	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/server"
)

// A template of Bedrock servers takes their versions and refuses what they don't have: Java,
// properties before their first start, plugins and extra ports.
func TestBedrockTemplate(t *testing.T) {
	s := newService(t)
	bedrock := func(change func(*Input)) Input {
		return input(func(in *Input) {
			in.Type, in.Version, in.Properties, in.ExtraPorts, in.Plugins, in.Versions, in.Channels = "bedrock", "1.26.61.1", nil, nil, nil, nil, nil
			change(in)
		})
	}
	if tmpl, err := s.Create(t.Context(), bedrock(func(*Input) {})); err != nil || tmpl.Type != "bedrock" || tmpl.Version != "1.26.61.1" {
		t.Fatalf("template = %+v, %v", tmpl, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Input)
		want   string
	}{
		{"version of Java", func(in *Input) { in.Version = "1.21.4" }, "Enter a version of Bedrock Dedicated Server like 1.26.61.1 or PREVIEW, or leave it empty for the latest."},
		{"Java", func(in *Input) { in.Java = "21" }, "Bedrock servers don't run Java: they have no Java version, Aikar's flags, JVM options, initial heap or mod loader."},
		{"properties", func(in *Input) { in.Properties = map[string]string{"gamemode": "creative"} }, "Bedrock servers write their server.properties when they start for the first time, so templates can't set its properties."},
		{"plugins", func(in *Input) { in.Plugins = []string{"luckperms"} }, "Vanilla and Bedrock servers can't load plugins or mods."},
		{"extra ports", func(in *Input) { in.ExtraPorts = []server.ExtraPort{{Port: 8100, Protocol: "tcp"}} }, "Bedrock servers load no plugins that listen at ports of their own, so they publish no extra ports."},
	} {
		if _, err := s.Create(t.Context(), bedrock(func(in *Input) { in.Name = tc.name; tc.change(in) })); err == nil || httpapi.Message(err) != tc.want {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
