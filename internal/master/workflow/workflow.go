// Package workflow runs workflows: chains of steps that triggers start, such as times, entries
// of the log, players who join, measures beyond a limit or calls of a webhook. Steps act on
// servers, players, networks and the outside world, and control the flow with conditions,
// loops, parallel branches, waits and variables; templates such as {{trigger.server.name}}
// pass the data of the trigger and of earlier steps on. Workflows run with the permissions of
// the user who saved them last, which each run checks again.
package workflow

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	noryxv1 "github.com/QwikByte/noryx/api/noryx/v1"
	"github.com/QwikByte/noryx/internal/logging"
	"github.com/QwikByte/noryx/internal/master/access"
	"github.com/QwikByte/noryx/internal/master/database"
	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/schedule"
)

const (
	maxWorkflows   = 500
	maxName        = 64
	maxDescription = 1000
	maxTriggers    = 10
	maxParams      = 20
	// maxSteps are all steps of a workflow, also those in branches and loops.
	maxSteps    = 250
	maxDepth    = 8
	maxBranches = 10
	maxCases    = 20
	maxEvery    = 7 * 24 * 60
	maxMinutes  = 24 * 60
	maxPlayers  = 100

	// Kinds of triggers.
	OnSchedule = "schedule"
	OnInterval = "interval"
	OnEvent    = "event"
	OnServer   = "server"
	OnMetric   = "metric"
	OnWebhook  = "webhook"
	// What starts runs besides triggers: a user, or another workflow.
	ByHand     = "manual"
	ByWorkflow = "workflow"

	// What a trigger does while the workflow runs.
	Skip     = "skip"
	Queue    = "queue"
	Parallel = "parallel"
)

var (
	idPattern       = regexp.MustCompile(`^[a-z2-7]{26}$`)
	categoryPattern = regexp.MustCompile(`^[a-z][a-z-]{0,31}$`)
	stepPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	namePattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
	errNotFound     = httpapi.Errorf(http.StatusNotFound, "Workflow not found. It may have been deleted.")
	serverEvents    = []string{"started", "stopped", "joined", "left"}
	measures        = []string{"players", "cpu", "memory", "tps", "disk"}
)

// Workflow starts its steps when one of its triggers fires, or when a user or another workflow
// runs it.
type Workflow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Definition
	// Hook tells whether a URL starts it, which only its creation shows.
	Hook      bool      `json:"hook"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// SavedBy is the user who saved it last, whose permissions its runs need.
	SavedBy string `json:"savedBy,omitempty"`
	LastRun *Run   `json:"lastRun,omitempty"`
	// NextRun is when a schedule or interval starts it next, if it is enabled.
	NextRun *time.Time `json:"nextRun,omitempty"`
	Running int        `json:"running"`
	author  int64      // the ID of SavedBy, 0 if the user was deleted or is disabled
}

// Definition is what a workflow does.
type Definition struct {
	Triggers []Trigger `json:"triggers"`
	// Params are what a user or another workflow passes when it runs the workflow, as
	// {{inputs.name}}.
	Params []Param `json:"params"`
	Steps  []Step  `json:"steps"`
	// Overlap is what a trigger does while the workflow runs: skip (the default), queue its
	// run, or run in parallel.
	Overlap string `json:"overlap"`
	// TimeZone is that of {{now}} and of waits until a time of day.
	TimeZone string `json:"timeZone"`
}

// Draft is a new or changed workflow.
type Draft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Definition
}

// Param is a value that a run gets.
type Param struct {
	Name string `json:"name"`
	// Type is text, number, boolean, or data such as a list, which text gives as JSON.
	Type        string `json:"type"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// Trigger starts a workflow. Its data, e.g. the server whose player joined, are {{trigger.…}}.
type Trigger struct {
	// Kind is schedule, interval, event, server, metric or webhook.
	Kind     string             `json:"kind"`
	Schedule *schedule.Schedule `json:"schedule,omitempty"`
	// Every is the minutes between the runs of an interval.
	Every uint32 `json:"every,omitempty"`
	// Targets are the servers whose entries, players or measures it watches; none for all.
	Targets []schedule.Target `json:"targets,omitempty"`
	// Level, Categories and Contains choose the entries of the log of an event: of at least the
	// level, of the categories (all if none) and with the text in their message.
	Level      string   `json:"level,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Contains   string   `json:"contains,omitempty"`
	// On is what a server trigger watches for: started, stopped, joined or left.
	On string `json:"on,omitempty"`
	// Players fire joined and left only for these names; none for all.
	Players []string `json:"players,omitempty"`
	// A metric fires once a Measure of a server was above, or Below, a Value for Minutes, and
	// again only once it was back meanwhile.
	Measure string  `json:"measure,omitempty"`
	Below   bool    `json:"below,omitempty"`
	Value   float64 `json:"value,omitempty"`
	Minutes uint32  `json:"minutes,omitempty"`
}

// Step is an action or controls the flow. Its ID names its output in templates, as
// {{steps.ID.…}}.
type Step struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Name is what the panel and the runs call it; empty for that of its kind.
	Name     string `json:"name,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
	// Continue goes on with the next step if it fails, instead of failing the run.
	Continue bool `json:"continue,omitempty"`
	// With are the settings of its kind.
	With json.RawMessage `json:"with,omitempty"`
	// Steps run within it: if its condition holds, for each item, repeatedly or with a catch.
	Steps []Step `json:"steps,omitempty"`
	// Else run if its condition doesn't hold, for no case of a switch, or if a try fails.
	Else []Step `json:"else,omitempty"`
	// Cases of a switch, and Branches that run in parallel.
	Cases    []Case   `json:"cases,omitempty"`
	Branches [][]Step `json:"branches,omitempty"`
}

// Case runs its steps if the value of a switch equals its own.
type Case struct {
	Value string `json:"value"`
	Steps []Step `json:"steps"`
}

// label is how messages name a step.
func (s Step) label() string { return fmt.Sprintf("%q", cmp.Or(s.Name, s.ID)) }

// bad is an error of settings for the panel.
func bad(format string, args ...any) error {
	return httpapi.Errorf(http.StatusBadRequest, format, args...)
}

// check validates a draft and puts it into its canonical form. It returns the permissions that
// the workflow needs on all servers.
func (d *Draft) check() ([]access.Permission, error) {
	d.Name, d.Description = strings.TrimSpace(d.Name), strings.TrimSpace(d.Description)
	d.Overlap = cmp.Or(d.Overlap, Skip)
	d.TimeZone = cmp.Or(d.TimeZone, "UTC")
	switch {
	case d.Name == "" || len(d.Name) > maxName || strings.ContainsFunc(d.Name, unicode.IsControl):
		return nil, bad("Enter a name with up to %d characters.", maxName)
	case len(d.Description) > maxDescription:
		return nil, bad("Keep the description within %d characters.", maxDescription)
	case !slices.Contains([]string{Skip, Queue, Parallel}, d.Overlap):
		return nil, bad("Choose what a trigger does while the workflow runs.")
	case len(d.Triggers) > maxTriggers:
		return nil, bad("Add up to %d triggers.", maxTriggers)
	case len(d.Params) > maxParams:
		return nil, bad("Add up to %d inputs.", maxParams)
	}
	if _, err := time.LoadLocation(d.TimeZone); err != nil {
		return nil, bad("Choose a time zone.")
	}
	c := checker{ids: map[string]bool{}, needs: map[access.Permission]bool{}}
	if d.Triggers == nil {
		d.Triggers = []Trigger{}
	}
	for i := range d.Triggers {
		if err := c.trigger(&d.Triggers[i]); err != nil {
			return nil, bad("Trigger %d: %s", i+1, message(err))
		}
	}
	if d.Params == nil {
		d.Params = []Param{}
	}
	names := map[string]bool{}
	for i := range d.Params {
		p := &d.Params[i]
		p.Description = strings.TrimSpace(p.Description)
		switch {
		case !namePattern.MatchString(p.Name):
			return nil, bad("Name each input with up to 32 letters, digits and underscores, starting with a letter.")
		case names[p.Name]:
			return nil, bad("There are two inputs named %s.", p.Name)
		case !slices.Contains([]string{"text", "number", "boolean", "data"}, p.Type):
			return nil, bad("Choose the type of the input %s.", p.Name)
		case len(p.Default) > maxTemplate || len(p.Description) > 200:
			return nil, bad("The input %s is too long.", p.Name)
		}
		names[p.Name] = true
		if _, err := p.value(p.Default); p.Default != "" && err != nil {
			return nil, bad("The default of %s: %s", p.Name, message(err))
		}
	}
	if d.Steps == nil {
		d.Steps = []Step{}
	}
	if err := c.steps(d.Steps, 1, false); err != nil {
		return nil, err
	}
	if len(d.Steps) == 0 {
		return nil, bad("Add a step.")
	}
	perms := []access.Permission{}
	for p := range c.needs {
		perms = append(perms, p)
	}
	slices.Sort(perms)
	return perms, nil
}

// value reads a value of the input from text, as the panel sends it, or as a step passes it.
func (p Param) value(v any) (any, error) {
	switch p.Type {
	case "number":
		n, ok := number(v)
		if !ok {
			return nil, bad("%q is no number.", text(v))
		}
		return n, nil
	case "boolean":
		switch strings.ToLower(text(v)) {
		case "true", "yes", "1", "on":
			return true, nil
		case "false", "no", "0", "off", "":
			return false, nil
		}
		return nil, bad("%q is neither true nor false.", text(v))
	case "data":
		s, ok := v.(string)
		var data any
		switch {
		case !ok:
			return normalize(v), nil
		case json.Unmarshal([]byte(s), &data) == nil:
			return data, nil
		}
		return s, nil // text is data too
	}
	return text(v), nil
}

// inputs returns the values of the inputs of a run: those given, or else the defaults.
func (d Definition) inputs(given map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, p := range d.Params {
		v, ok := given[p.Name]
		if !ok || v == nil || v == "" {
			if p.Required && p.Default == "" {
				return nil, bad("Enter the input %s.", p.Name)
			}
			v = p.Default
		}
		value, err := p.value(v)
		if err != nil {
			return nil, bad("The input %s: %s", p.Name, message(err))
		}
		out[p.Name] = value
	}
	return out, nil
}

// checker checks the triggers and steps of a workflow, and collects what they need.
type checker struct {
	ids   map[string]bool
	count int
	needs map[access.Permission]bool
}

func (c *checker) need(perms ...access.Permission) {
	for _, p := range perms {
		c.needs[p] = true
	}
}

func (c *checker) trigger(t *Trigger) error {
	var err error
	if t.Targets, err = targets(t.Targets); err != nil {
		return err
	}
	keep := *t
	*t = Trigger{Kind: t.Kind}
	switch t.Kind {
	case OnSchedule:
		if keep.Schedule == nil {
			return bad("Choose when it runs.")
		}
		t.Schedule = keep.Schedule
		if msg := t.Schedule.Normalize(); msg != "" {
			return bad("%s", msg)
		}
	case OnInterval:
		t.Every = keep.Every
		if t.Every == 0 || t.Every > maxEvery {
			return bad("Run every 1 to %d minutes.", maxEvery)
		}
	case OnEvent:
		t.Level, t.Contains, t.Targets = keep.Level, strings.TrimSpace(keep.Contains), keep.Targets
		if level, err := logging.ParseLevel(t.Level); err != nil || level < slog.LevelInfo {
			return bad("Choose the level info, warn or error.")
		}
		t.Categories = []string{}
		for _, cat := range keep.Categories {
			if !categoryPattern.MatchString(cat) {
				return bad("%q isn't a category of the log.", cat)
			}
			if !slices.Contains(t.Categories, cat) {
				t.Categories = append(t.Categories, cat)
			}
		}
		if len(t.Contains) > 200 {
			return bad("Look for up to 200 characters in the message.")
		}
		// The entries tell of any server, so whoever starts the workflow sees them all.
		c.need(access.LogsView)
	case OnServer:
		t.On, t.Targets = keep.On, keep.Targets
		if !slices.Contains(serverEvents, t.On) {
			return bad("Choose whether servers start or stop, or players join or leave.")
		}
		if t.On == "joined" || t.On == "left" {
			t.Players = []string{}
			for _, name := range keep.Players {
				if name = strings.TrimSpace(name); !noryxv1.ValidPlayerName(name) {
					return bad("%q is no name of a player.", name)
				}
				t.Players = append(t.Players, name)
			}
			if len(t.Players) > maxPlayers {
				return bad("Choose up to %d players.", maxPlayers)
			}
		}
		c.need(access.ServersView)
	case OnMetric:
		t.Measure, t.Below, t.Value, t.Minutes, t.Targets = keep.Measure, keep.Below, keep.Value, keep.Minutes, keep.Targets
		if !slices.Contains(measures, t.Measure) {
			return bad("Choose a measure.")
		}
		if t.Minutes > 60 {
			return bad("Wait up to 60 minutes.")
		}
		c.need(access.ServersView)
	case OnWebhook:
	default:
		return bad("Choose what starts the workflow.")
	}
	return nil
}

// targets checks the servers of a trigger or step, which may be none.
func targets(in []schedule.Target) ([]schedule.Target, error) {
	if len(in) == 0 {
		return nil, nil
	}
	return schedule.CheckTargets(in)
}

// steps checks steps, of which those that are off, or within one that is, may be incomplete, e.g.
// once what they act on was deleted: they don't run, and are checked once they are turned on.
func (c *checker) steps(list []Step, depth int, off bool) error {
	for i := range list {
		if err := c.step(&list[i], depth, off || list[i].Disabled); err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) step(s *Step, depth int, off bool) error {
	c.count++
	s.Name = strings.TrimSpace(s.Name)
	k, ok := kinds[s.Kind]
	switch {
	case c.count > maxSteps:
		return bad("A workflow has up to %d steps.", maxSteps)
	case depth > maxDepth:
		return bad("Steps go up to %d deep.", maxDepth)
	case !stepPattern.MatchString(s.ID):
		return bad("Give each step an ID of up to 32 small letters, digits and underscores, e.g. restart_1.")
	case c.ids[s.ID]:
		return bad("Two steps have the ID %s.", s.ID)
	case len(s.Name) > maxName || strings.ContainsFunc(s.Name, unicode.IsControl):
		return bad("Step %s: Keep its name within %d characters.", s.ID, maxName)
	case !ok:
		return bad("Step %s: Choose what it does.", s.label())
	}
	c.ids[s.ID] = true
	settings, err := k.decode(s.With)
	if err == nil {
		err = checkTemplates(settings)
	}
	switch {
	case err != nil && !off:
		return bad("Step %s: %s", s.label(), message(err))
	case err == nil:
		if s.With, err = json.Marshal(settings); err != nil {
			return err
		}
		if !off {
			c.need(k.needs(settings)...)
		}
	}
	if !k.steps {
		s.Steps = nil
	}
	if !k.elses {
		s.Else = nil
	}
	if !k.cases {
		s.Cases = nil
	}
	if !k.branches {
		s.Branches = nil
	}
	switch {
	case len(s.Cases) > maxCases:
		return bad("Step %s: Add up to %d cases.", s.label(), maxCases)
	case k.branches && (len(s.Branches) < 2 || len(s.Branches) > maxBranches):
		return bad("Step %s: Run 2 to %d branches in parallel.", s.label(), maxBranches)
	}
	for _, list := range [][]Step{s.Steps, s.Else} {
		if err := c.steps(list, depth+1, off); err != nil {
			return err
		}
	}
	for i := range s.Cases {
		if _, err := parse(s.Cases[i].Value); err != nil {
			return bad("Step %s: The value of case %d: %s.", s.label(), i+1, err)
		}
		if err := c.steps(s.Cases[i].Steps, depth+1, off); err != nil {
			return err
		}
	}
	for _, b := range s.Branches {
		if err := c.steps(b, depth+1, off); err != nil {
			return err
		}
	}
	return nil
}

// checkTemplates parses the templates in settings.
func checkTemplates(settings any) error {
	raw, err := json.Marshal(settings)
	var v any
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	if err != nil {
		return err
	}
	return templates(v, func(s string) error {
		if _, err := parse(s); err != nil {
			return bad("The template %q is invalid: %s.", database.Clip(s, 60), err)
		}
		return nil
	})
}

// walk calls fn for each step, also those within others.
func walk(steps []Step, fn func(*Step)) {
	for i := range steps {
		s := &steps[i]
		fn(s)
		walk(s.Steps, fn)
		walk(s.Else, fn)
		for j := range s.Cases {
			walk(s.Cases[j].Steps, fn)
		}
		for _, b := range s.Branches {
			walk(b, fn)
		}
	}
}
