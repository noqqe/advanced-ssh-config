package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noqqe/advanced-ssh-config/pkg/flexyaml"
	. "github.com/noqqe/advanced-ssh-config/pkg/logger"
	"github.com/noqqe/advanced-ssh-config/pkg/utils"
	"github.com/noqqe/advanced-ssh-config/pkg/version"
)

var asshBinaryPath = "assh"

const defaultSshConfigPath = "~/.ssh/config"

// Config contains a list of Hosts sections and a Defaults section representing a configuration file
type Config struct {
	Hosts             HostsMap `yaml:"hosts,omitempty,flow" json:"hosts"`
	Templates         HostsMap `yaml:"templates,omitempty,flow" json:"templates"`
	Defaults          Host     `yaml:"defaults,omitempty,flow" json:"defaults,omitempty"`
	Includes          []string `yaml:"includes,omitempty,flow" json:"includes,omitempty"`
	ASSHKnownHostFile string   `yaml:"asshknownhostfile,omitempty,flow" json:"asshknownhostfile,omitempty"`
	ASSHBinaryPath    string   `yaml:"asshbinarypath,omitempty,flow" json:"asshbinarypath,omitempty"`

	includedFiles map[string]bool
	sshConfigPath string
}

// SetASSHBinaryPath sets the default assh binary path
// this value may be overwritten in the assh.yml file using the asshbinarypath variable
func SetASSHBinaryPath(path string) {
	asshBinaryPath = path
}

// String returns the JSON output
func (c *Config) String() string {
	s, _ := json.Marshal(c)
	return string(s)
}

// SaveNewKnownHost registers the target as a new known host and save the full known hosts list on disk
func (c *Config) SaveNewKnownHost(target string) {
	c.addKnownHost(target)

	path, err := utils.ExpandUser(c.ASSHKnownHostFile)
	if err != nil {
		Logger.Errorf("Cannot append host %q, unknown ASSH known_hosts file: %v", target, err)
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0660)
	if err != nil {
		Logger.Errorf("Cannot append host %q to %q (performance degradation): %v", target, c.ASSHKnownHostFile, err)
		return
	}

	fmt.Fprintln(file, target)

	file.Close()
}

func (c *Config) addKnownHost(target string) {
	host := c.GetHostSafe(target)
	if inst, ok := c.Hosts[host.pattern]; ok {
		inst.AddKnownHost(target)
	}
}

// LoadKnownHosts loads known hosts list from disk
func (c *Config) LoadKnownHosts() error {
	path, err := utils.ExpandUser(c.ASSHKnownHostFile)
	if err != nil {
		return err
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		c.addKnownHost(scanner.Text())
	}

	return scanner.Err()
}

// IncludedFiles returns the list of the included files
func (c *Config) IncludedFiles() []string {
	includedFiles := []string{}
	for file := range c.includedFiles {
		includedFiles = append(includedFiles, file)
	}
	return includedFiles
}

// JsonString returns a string representing the JSON of a Config object
func (c *Config) JsonString() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// computeHost returns a copy of the host with applied defaults, resolved inheritances and configured internal fields
func computeHost(host *Host, config *Config, name string, fullCompute bool) (*Host, error) {
	computedHost := NewHost(name)
	computedHost.pattern = name
	if host != nil {
		*computedHost = *host
	}

	// name internal field
	computedHost.name = name
	computedHost.inherited = make(map[string]bool, 0)
	// self is already inherited
	computedHost.inherited[name] = true

	// Inheritance
	// FIXME: allow deeper inheritance:
	//     currently not resolving inherited hosts
	//     we should resolve all inherited hosts and pass the
	//     currently resolved hosts to avoid computing an host twice
	for _, name := range host.Inherits {
		_, found := computedHost.inherited[name]
		if found {
			Logger.Debugf("Detected circular loop inheritance, skiping...")
			continue
		}
		computedHost.inherited[name] = true

		target, err := config.getHostByPath(name, false, false, true)
		if err != nil {
			Logger.Warnf("Cannot inherits from %q: %v", name, err)
			continue
		}
		computedHost.ApplyDefaults(target)
	}

	// fullCompute applies config.Defaults
	// config.Defaults should be applied when proxying
	// but should not when exporting .ssh/config file
	if fullCompute {
		// apply defaults based on "Host *"
		computedHost.ApplyDefaults(&config.Defaults)

		if computedHost.HostName == "" {
			computedHost.HostName = name
		}
		// expands variables in host
		// i.e: %h.some.zone -> {name}.some.zone
		hostname := strings.Replace(computedHost.HostName, "%h", "%n", -1)

		// ssh resolve '%h' in hostnames
		// -> we bypass the string expansion if the input matches
		//    an already resolved hostname
		// See https://github.com/noqqe/advanced-ssh-config/issues/103
		pattern := strings.Replace(hostname, "%n", "*", -1)
		if match, _ := path.Match(pattern, computedHost.inputName); match {
			computedHost.HostName = computedHost.inputName
		} else {
			computedHost.HostName = computedHost.ExpandString(hostname)
		}
	}

	return computedHost, nil
}

func (c *Config) getHostByName(name string, safe bool, compute bool, allowTemplate bool) (*Host, error) {
	if host, ok := c.Hosts[name]; ok {
		Logger.Debugf("getHostByName direct matching: %q", name)
		return computeHost(host, c, name, compute)
	}

	for origPattern, host := range c.Hosts {
		patterns := append([]string{origPattern}, host.Aliases...)
		for _, pattern := range patterns {

			matched, err := path.Match(pattern, name)
			if err != nil {
				return nil, err
			}
			if matched {
				Logger.Debugf("getHostByName pattern matching: %q => %q", pattern, name)
				return computeHost(host, c, name, compute)
			}
		}
	}

	if allowTemplate {
		for pattern, template := range c.Templates {
			matched, err := path.Match(pattern, name)
			if err != nil {
				return nil, err
			}
			if matched {
				return computeHost(template, c, name, compute)
			}
		}
	}

	if safe {
		host := NewHost(name)
		host.HostName = name
		return computeHost(host, c, name, compute)
	}

	return nil, fmt.Errorf("no such host: %s", name)
}

func (c *Config) getHostByPath(path string, safe bool, compute bool, allowTemplate bool) (*Host, error) {
	parts := strings.SplitN(path, "/", 2)

	host, err := c.getHostByName(parts[0], safe, compute, allowTemplate)
	if err != nil {
		return nil, err
	}

	if len(parts) > 1 {
		host.Gateways = []string{parts[1]}
	}

	return host, nil
}

// GetGatewaySafe returns gateway Host configuration, a gateway is like a Host, except, the host path is not resolved
func (c *Config) GetGatewaySafe(name string) *Host {
	host, err := c.getHostByName(name, true, true, false) // FIXME: fullCompute for gateway ?
	if err != nil {
		panic(err)
	}
	return host
}

// GetHost returns a matching host form Config hosts list
func (c *Config) GetHost(name string) (*Host, error) {
	return c.getHostByPath(name, false, true, false)
}

// GetHostSafe won't fail, in case the host is not found, it will returns a virtual host matching the pattern
func (c *Config) GetHostSafe(name string) *Host {
	host, err := c.getHostByPath(name, true, true, false)
	if err != nil {
		panic(err)
	}
	return host
}

// isSSHConfigOutdated returns true if assh.yml or an included file has a
// modification date more recent than .ssh/config
func (c *Config) isSSHConfigOutdated() (bool, error) {
	filepath, err := utils.ExpandUser(c.sshConfigPath)
	if err != nil {
		return false, err
	}
	sshConfigStat, err := os.Stat(filepath)
	if err != nil {
		return false, err
	}
	sshConfigModTime := sshConfigStat.ModTime()

	for filepath := range c.includedFiles {
		asshConfigStat, err := os.Stat(filepath)
		if err != nil {
			return false, err
		}
		if asshConfigStat.ModTime().After(sshConfigModTime) {
			return true, nil
		}
	}
	return false, nil
}

// IsConfigOutdated returns true if .ssh/config needs to be rebuild.
// The reason may be:
// - assh.yml (or an included file) was updated recently
// - <target> matches a regex and was never seen before (not present in known-hosts file)
func (c *Config) IsConfigOutdated(target string) (bool, error) {
	// check if the target is a regex and if the pattern
	// was never matched before (not in known hosts)
	if c.needsARebuildForTarget(target) {
		c.SaveNewKnownHost(target)
		return true, nil
	}

	// check if the ~/.ssh/config file is older than assh.yml or any included file
	return c.isSSHConfigOutdated()
}

// needsARebuildForTarget returns true if the .ssh/config file needs to be rebuild for a specific target
func (c *Config) needsARebuildForTarget(target string) bool {
	parts := strings.Split(target, "/")

	// compute lists
	aliases := map[string]bool{}
	for _, host := range c.Hosts {
		for _, alias := range host.Aliases {
			aliases[alias] = true
		}
		for _, knownHost := range host.knownHosts {
			aliases[knownHost] = true
		}
	}

	patterns := []string{}
	for origPattern, host := range c.Hosts {
		patterns = append(patterns, origPattern)
		patterns = append(patterns, host.Aliases...)
	}

	for _, part := range parts {
		// check for direct hostname matching
		if _, ok := c.Hosts[part]; ok {
			continue
		}

		// check for direct alias matching
		if _, ok := aliases[part]; ok {
			continue
		}

		// check for pattern matching
		for _, pattern := range patterns {
			matched, err := path.Match(pattern, part)
			if err != nil {
				continue
			}
			if matched {
				return true
			}
		}
	}

	return false
}

// LoadConfig loads the content of an io.Reader source
func (c *Config) LoadConfig(source io.Reader) error {
	buf, err := ioutil.ReadAll(source)
	if err != nil {
		return err
	}
	err = flexyaml.Unmarshal(buf, &c)
	if err != nil {
		return err
	}
	c.applyMissingNames()
	return nil
}

func (c *Config) applyMissingNames() {
	for key, host := range c.Hosts {
		if host == nil {
			c.Hosts[key] = &Host{}
			host = c.Hosts[key]
		}
		host.pattern = key
		host.name = key // should be removed
	}
	for key, template := range c.Templates {
		if template == nil {
			c.Templates[key] = &Host{}
			template = c.Templates[key]
		}
		template.pattern = key
		template.name = key // should be removed
		template.isTemplate = true
	}
	c.Defaults.isDefault = true
}

// SaveSSHConfig saves the configuration to ~/.ssh/config
func (c *Config) SaveSSHConfig() error {
	if c.sshConfigPath == "" {
		return fmt.Errorf("no Config.sshConfigPath configured")
	}
	filepath, err := utils.ExpandUser(c.sshConfigPath)
	if err != nil {
		return err
	}
	file, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer file.Close()
	Logger.Debugf("Writing SSH config file to %q", filepath)
	return c.WriteSSHConfigTo(file)
}

// LoadFile loads the content of a configuration file in the Config object
func (c *Config) LoadFile(filename string) error {
	beforeHostsCount := len(c.Hosts)

	// Resolve '~' and '$HOME'
	filepath, err := utils.ExpandUser(filename)
	if err != nil {
		return err
	}

	// Anti-loop protection
	if _, ok := c.includedFiles[filepath]; ok {
		return nil
	}
	c.includedFiles[filepath] = false

	Logger.Debugf("Loading config file '%s'", filepath)

	// Read file
	source, err := os.Open(filepath)
	if err != nil {
		return err
	}

	// Load config stream
	err = c.LoadConfig(source)
	if err != nil {
		return err
	}

	// Successful loading
	c.includedFiles[filepath] = true
	afterHostsCount := len(c.Hosts)
	diffHostsCount := afterHostsCount - beforeHostsCount
	Logger.Debugf("Loaded config file '%s' (%d + %d => %d hosts)", filepath, beforeHostsCount, afterHostsCount, diffHostsCount)

	// Handling includes
	for _, include := range c.Includes {
		if err = c.LoadFiles(include); err != nil {
			return err
		}
	}

	return nil
}

// LoadFiles will try to glob the pattern and load each matching entries
func (c *Config) LoadFiles(pattern string) error {
	// Resolve '~' and '$HOME'
	expandedPattern, err := utils.ExpandUser(pattern)
	if err != nil {
		return err
	}

	// Globbing
	filepaths, err := filepath.Glob(expandedPattern)
	if err != nil {
		return err
	}

	// Load files iteratively
	for _, filepath := range filepaths {
		if err := c.LoadFile(filepath); err != nil {
			Logger.Warnf("Cannot include %q: %v", filepath, err)
		}
	}

	if c.ASSHBinaryPath != "" {
		path, err := utils.ExpandUser(c.ASSHBinaryPath)
		if err != nil {
			return err
		}
		asshBinaryPath = path
	}
	return nil
}

// sortedNames returns the host names sorted alphabetically
func (c *Config) sortedNames() []string {
	names := sort.StringSlice{}
	for key := range c.Hosts {
		names = append(names, key)
	}
	sort.Sort(names)
	return names
}

// WriteSSHConfigTo returns a .ssh/config valid file containing assh configuration
func (c *Config) WriteSSHConfigTo(w io.Writer) error {
	header := strings.TrimSpace(`
# This file was automatically generated by assh v%VERSION
# on %BUILD_DATE, based on ~/.ssh/assh.yml
#
# more info: https://github.com/noqqe/advanced-ssh-config
`)
	header = strings.Replace(header, "%VERSION", version.VERSION, -1)
	header = strings.Replace(header, "%BUILD_DATE", time.Now().Format("2006-01-02 15:04:05 -0700 MST"), -1)
	fmt.Fprintln(w, header)
	// FIXME: add version
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# host-based configuration")
	for _, name := range c.sortedNames() {
		host := c.Hosts[name]
		computedHost, err := computeHost(host, c, name, false)
		if err != nil {
			return err
		}
		computedHost.WriteSSHConfigTo(w)
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "# global configuration")
	c.Defaults.name = "*"
	c.Defaults.WriteSSHConfigTo(w)

	return nil
}

// New returns an instantiated Config object
func New() *Config {
	var config Config
	config.Hosts = make(map[string]*Host)
	config.Templates = make(map[string]*Host)
	config.includedFiles = make(map[string]bool)
	config.sshConfigPath = defaultSshConfigPath
	config.ASSHKnownHostFile = "~/.ssh/assh_known_hosts"
	config.ASSHBinaryPath = ""
	return &config
}

// Open parses a configuration file and returns a *Config object
func Open(path string) (*Config, error) {
	config := New()
	err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	return config, nil
}
