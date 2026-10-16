package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	configFile = "goletter.toml"
	stateDir   = ".goletter"
)

// Config is goletter.toml: the shared, committable project settings.
type Config struct {
	Profile  string `toml:"profile"`
	Region   string `toml:"region"`
	From     string `toml:"from"`
	List     string `toml:"list"`
	Topic    string `toml:"topic"`
	Template string `toml:"template"`
}

const configHeader = `# goletter project. Commit this file; device-local state lives in .goletter/ (ignored).
#   profile   AWS profile that "aws login" creates for goletter, "goletter" when empty
#   region    AWS region of the SES account
#   from      sender, a verified SES identity, e.g. "News <news@example.com>"
#   list      SES contact list name
#   topic     SES topic to send to, empty for the whole list
#   template  default template in templates/ for new campaigns, empty for none
`

// profile is the AWS profile goletter logs in to. It is its own profile so
// that keys configured elsewhere on the machine are never picked up.
func (c Config) profile() string { return cmp.Or(c.Profile, "goletter") }

// Project is an initialized goletter directory.
type Project struct {
	Dir string
	Config
	secret []byte // pseudonymizes addresses in local state, see pseudonym
}

var errNotInitialized = errors.New("not a goletter directory")

func openProject(dir string) (*Project, error) {
	p := &Project{Dir: dir}
	data, err := os.ReadFile(p.path(configFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotInitialized
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &p.Config); err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	return p, nil
}

// initProject creates the directory layout, a simple template and the
// .gitignore entry for local state, then writes the config.
func initProject(dir string, cfg Config) (*Project, error) {
	p := &Project{Dir: dir, Config: cfg}
	for _, d := range []string{"templates", "campaigns", stateDir} {
		if err := os.MkdirAll(p.path(d), 0o755); err != nil {
			return nil, err
		}
	}
	if err := writeIfMissing(p.path("templates", "Simple.html"), simpleTemplate); err != nil {
		return nil, err
	}
	if err := ignore(p.path(".gitignore"), stateDir+"/"); err != nil {
		return nil, err
	}
	return p, p.save()
}

func (p *Project) save() error {
	return writeTOML(p.path(configFile), configHeader, p.Config)
}

func (p *Project) path(parts ...string) string {
	return filepath.Join(append([]string{p.Dir}, parts...)...)
}

func (p *Project) name() string { return filepath.Base(p.Dir) }

// consoleURL deep-links into the SES console for the project's region.
func (p *Project) consoleURL(page string) string {
	return fmt.Sprintf("https://%s.console.aws.amazon.com/ses/home?region=%s#/%s", p.Region, p.Region, page)
}

func writeTOML(path, header string, v any) error {
	data, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(header), data...), 0o644)
}

func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// ignore adds line to the .gitignore at path unless it is already there.
func ignore(path, line string) error {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(path, append(data, line+"\n"...), 0o644)
}

// listTopic names the sending target, e.g. "news / weekly".
func (p *Project) listTopic() string {
	if p.Topic != "" {
		return p.List + " / " + p.Topic
	}
	return p.List
}
