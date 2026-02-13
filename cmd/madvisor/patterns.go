package main

import (
	"embed"
	"fmt"
	"os"
	"regexp"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed patterns_default.yaml
var defaultPatternsFS embed.FS

type UnitEntry struct {
	Unit     string   `yaml:"unit"`
	Suffix   string   `yaml:"suffix"`
	Matchers []string `yaml:"matchers"`
}

type UnitsConfig struct {
	Units []UnitEntry `yaml:"units"`
}

type compiledUnit struct {
	unit   string
	suffix string
	re     *regexp.Regexp
}

type UnitMatcher struct {
	mu    sync.RWMutex
	units []compiledUnit
}

type UnitMatch struct {
	Unit   string
	Suffix string
}

func loadUnitsConfig(data []byte) (*UnitsConfig, error) {
	var cfg UnitsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse units YAML: %w", err)
	}
	return &cfg, nil
}

func loadDefaultUnits() (*UnitsConfig, error) {
	data, err := defaultPatternsFS.ReadFile("patterns_default.yaml")
	if err != nil {
		return nil, fmt.Errorf("read embedded units: %w", err)
	}
	return loadUnitsConfig(data)
}

func loadUnitsFile(path string) (*UnitsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read units file %q: %w", path, err)
	}
	return loadUnitsConfig(data)
}

func mergeUnits(base, override *UnitsConfig) *UnitsConfig {
	if override == nil {
		return base
	}

	merged := &UnitsConfig{}
	seen := make(map[string]bool)

	for _, u := range override.Units {
		merged.Units = append(merged.Units, u)
		seen[u.Unit] = true
	}

	for _, u := range base.Units {
		if seen[u.Unit] {
			continue
		}
		merged.Units = append(merged.Units, u)
	}

	return merged
}

func compileUnits(cfg *UnitsConfig) (*UnitMatcher, error) {
	um := &UnitMatcher{}
	for _, entry := range cfg.Units {
		for _, expr := range entry.Matchers {
			re, err := regexp.Compile(expr)
			if err != nil {
				return nil, fmt.Errorf("compile pattern %q for unit %q: %w", expr, entry.Unit, err)
			}
			um.units = append(um.units, compiledUnit{
				unit:   entry.Unit,
				suffix: entry.Suffix,
				re:     re,
			})
		}
	}
	return um, nil
}

func (um *UnitMatcher) Match(name string) *UnitMatch {
	um.mu.RLock()
	defer um.mu.RUnlock()
	for _, cu := range um.units {
		if cu.re.MatchString(name) {
			return &UnitMatch{Unit: cu.unit, Suffix: cu.suffix}
		}
	}
	return nil
}

var globalUnitMatcher *UnitMatcher

func initPatterns(userFile string) error {
	base, err := loadDefaultUnits()
	if err != nil {
		return err
	}

	var user *UnitsConfig
	if userFile != "" {
		user, err = loadUnitsFile(userFile)
		if err != nil {
			return err
		}
	}

	merged := mergeUnits(base, user)
	um, err := compileUnits(merged)
	if err != nil {
		return err
	}
	globalUnitMatcher = um
	return nil
}
