package config

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"git.mehdix.org/alborz"
	"github.com/BurntSushi/toml"
)

//go:embed config.example.toml
var configExample string

type File struct {
	path   string
	raw    []byte
	values map[string]any
}

func Load(fs *flag.FlagSet, lookup func(string) (string, bool)) (*File, error) {
	path := fs.Lookup("config").Value.String()
	if value, ok := lookup("LBRZ_CONFIG"); ok {
		path = value
	}
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(dir, "alborz", "config.toml")
	}
	c := &File{path: path, values: make(map[string]any)}
	if path == "none" {
		c.path = ""
		return c, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		c.raw = raw
		if _, err := toml.Decode(string(raw), &c.values); err != nil {
			if syntax, ok := errors.AsType[toml.ParseError](err); ok {
				return nil, fmt.Errorf("invalid configuration in %s at line %d (check TOML syntax)", path, syntax.Position.Line)
			}
			return nil, fmt.Errorf("invalid configuration in %s (check TOML syntax)", path)
		}
	}
	explicit := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for name, value := range c.values {
		if name == "domains" || name == "provider" {
			continue
		}
		if name == "config" || fs.Lookup(name) == nil {
			return nil, fmt.Errorf("%s: unknown setting %q", path, name)
		}
		switch value.(type) {
		case string, bool, int64, float64:
		default:
			return nil, fmt.Errorf("%s: setting %q must be a scalar value", path, name)
		}
		if !explicit[name] {
			if err := fs.Set(name, fmt.Sprint(value)); err != nil {
				return nil, fmt.Errorf("%s: invalid value for %s", path, name)
			}
		}
	}
	return c, nil
}

func ApplyEnvironment(fs *flag.FlagSet, lookup func(string) (string, bool)) error {
	var failure error
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "config" {
			return
		}
		name := "LBRZ_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if value, ok := lookup(name); ok {
			if err := fs.Set(f.Name, value); err != nil {
				failure = fmt.Errorf("invalid value for %s", name)
			}
		}
	})
	return failure
}

func (c *File) Servers(options *alborz.Options, upstreams []string, lookup func(string) (string, bool)) error {
	if value, ok := c.values["domains"]; ok {
		domains, ok := value.(map[string]any)
		if !ok {
			return errors.New("domains must contain named domain tables")
		}
		options.Domains = make(map[string]alborz.DomainConfig)
		for domain, value := range domains {
			services, err := serverValues(value)
			if err != nil {
				return fmt.Errorf("domain %q: %w", domain, err)
			}
			options.Domains[domain] = services
		}
	}
	if value, ok := c.values["provider"]; ok {
		var err error
		if options.Provider, err = serverValues(value); err != nil {
			return err
		}
	}
	if err := options.ParseUpstreams(upstreams); err != nil {
		return err
	}
	domainList, domainListSet := lookup("LBRZ_DOMAINS")
	if domainListSet {
		previous := options.Domains
		options.Provider = nil
		options.Domains = make(map[string]alborz.DomainConfig)
		for _, domain := range strings.FieldsFunc(domainList, func(r rune) bool { return r == ',' || r == ' ' }) {
			options.Domains[domain] = previous[domain]
		}
	}
	provider := alborz.DomainConfig{}
	for _, service := range configServices {
		if value, ok := lookup("LBRZ_PROVIDER_" + strings.ToUpper(service)); ok {
			provider[service] = value
		}
	}
	if len(provider) > 0 {
		if domainListSet && len(options.Domains) > 0 {
			return errors.New("cannot mix LBRZ_DOMAINS with LBRZ_PROVIDER_* servers")
		}
		options.Domains = nil
		if options.Provider == nil {
			options.Provider = alborz.DomainConfig{}
		}
		maps.Copy(options.Provider, provider)
	}
	names := make(map[string][]string)
	for domain := range options.Domains {
		names[envDomain(domain)] = append(names[envDomain(domain)], domain)
	}
	for prefix, domains := range names {
		for _, service := range configServices {
			name := "LBRZ_DOMAIN_" + prefix + "_" + strings.ToUpper(service)
			if value, ok := lookup(name); ok {
				if len(domains) != 1 {
					return fmt.Errorf("%s is ambiguous between configured domains", name)
				}
				domain := domains[0]
				if options.Domains[domain] == nil {
					options.Domains[domain] = alborz.DomainConfig{}
				}
				options.Domains[domain][service] = value
			}
		}
	}

	return nil
}

var configServices = []string{"imap", "smtp", "caldav", "carddav", "sieve", "poppassd"}

func envDomain(domain string) string {
	return strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(domain))
}

func serverValues(value any) (alborz.DomainConfig, error) {
	table, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("server settings must be a table")
	}
	services := alborz.DomainConfig{}
	for name, value := range table {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a URL string", name)
		}
		services[name] = text
	}
	return services, nil
}

func (c *File) EnsureKey(options *alborz.Options) error {
	if options.LoginKey != "" {
		return nil
	}
	if c.path == "" {
		return errors.New("set LBRZ_LOGIN_KEY when configuration files are disabled")
	}
	if _, err := os.Stat(filepath.Join(options.DataDir, "alborz.db")); err == nil {
		return errors.New("existing data has no login key: restore the original configuration or set LBRZ_LOGIN_KEY")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, exists := c.values["login-key"]; exists {
		return errors.New("login-key is empty: remove the setting to generate a key for a fresh installation")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	base := c.raw
	if base == nil {
		base = []byte(configExample)
	}
	var prefix bytes.Buffer
	if err := toml.NewEncoder(&prefix).Encode(map[string]string{"login-key": encoded}); err != nil {
		return err
	}
	if err := c.write(append(prefix.Bytes(), base...)); err != nil {
		return err
	}
	c.values["login-key"], options.LoginKey = encoded, encoded
	return nil
}

func (c *File) SaveSetup(domains map[string]alborz.DomainConfig, site alborz.SetupSite) error {
	values := map[string]any{"domains": domains}
	if site.PublicURL != "" {
		values["public-url"] = site.PublicURL
	}
	if len(site.TrustedProxies) > 0 {
		values["trusted-proxy"] = strings.Join(site.TrustedProxies, ", ")
	}
	// A key the file names already wins, even empty; writing it twice is invalid TOML.
	for name := range values {
		if _, set := c.values[name]; set && name != "domains" {
			delete(values, name)
		}
	}
	// The encoder writes the bare keys before the [domains] tables, as TOML needs.
	var section bytes.Buffer
	if err := toml.NewEncoder(&section).Encode(values); err != nil {
		return err
	}
	if c.path == "" {
		return fmt.Errorf("configuration files are disabled; configure the domain using environment variables")
	}
	if c.values["domains"] != nil || c.values["provider"] != nil {
		return errors.New("configuration already contains server settings")
	}
	base := c.raw
	if base == nil {
		base = []byte(configExample)
	}
	if err := c.write(append(append(bytes.Clone(base), '\n'), section.Bytes()...)); err != nil {
		return err
	}
	maps.Copy(c.values, values)
	return nil
}

// Keep existing comments and refuse to replace a file changed since it was read.
func (c *File) write(raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(c.path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("cannot lock configuration %s: %w", c.path, err)
	}
	lock.Close()
	defer os.Remove(c.path + ".lock")
	current, err := os.ReadFile(c.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !reflect.DeepEqual(current, c.raw) {
		return fmt.Errorf("configuration %s changed; restart Alborz before completing setup", c.path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".alborz-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	closed := tmp.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err := os.Rename(tmp.Name(), c.path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(c.path))
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
	}
	c.raw = bytes.Clone(raw)
	return nil
}
