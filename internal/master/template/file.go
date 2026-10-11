package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/QwikByte/noryx/internal/master/httpapi"
)

const (
	// fileFormat marks the files of exported templates, and fileVersion is the version of their
	// format, which changes whenever the format does: 2 gave plugins their channel, 3 servers
	// their extra ports.
	fileFormat  = "noryx-template"
	fileVersion = 3
	// maxFile is as much as any request with a template.
	maxFile = 1 << 20
)

// File is an exported template: what it sets up, without IDs of the master, nodes or servers.
type File struct {
	Format   string  `json:"format"`
	Version  int     `json:"version"`
	Template Content `json:"template"`
}

// Export returns a template as a file.
func Export(t Template) File { return File{fileFormat, fileVersion, t.Content} }

// readFile reads an exported template into the input that saves it, so that an import is
// checked like any template saved in the panel.
func readFile(w http.ResponseWriter, r *http.Request) (Input, error) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFile))
	if tooLarge := new(http.MaxBytesError); errors.As(err, &tooLarge) {
		return Input{}, httpapi.Errorf(http.StatusRequestEntityTooLarge, "Template files have up to 1 MiB.")
	} else if err != nil {
		return Input{}, err
	}
	return parseFile(data)
}

func parseFile(data []byte) (Input, error) {
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	switch err := json.Unmarshal(data, &head); {
	case err != nil || head.Format != fileFormat || head.Version < 1:
		return Input{}, httpapi.Errorf(http.StatusBadRequest, "This isn't a template exported from Noryx.")
	case head.Version > fileVersion:
		return Input{}, httpapi.Errorf(http.StatusBadRequest, "The template comes from a newer version of Noryx. Update Noryx to import it.")
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Input{}, httpapi.Errorf(http.StatusBadRequest, "The template file is invalid: %v", err)
	}
	c := f.Template
	in := Input{
		Name: c.Name, Description: c.Description, Settings: c.Settings, Tags: c.Tags, Plugins: []string{}, Versions: map[string]string{}, Channels: map[string]string{},
	}
	for _, p := range c.Plugins {
		in.Plugins = append(in.Plugins, p.ID)
		if p.Version != "" {
			in.Versions[p.ID] = p.Version
		}
		if p.Channel != "" {
			in.Channels[p.ID] = p.Channel
		}
	}
	return in, nil
}
