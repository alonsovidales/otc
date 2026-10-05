// SPDX-License-Identifier: AGPL-3.0-or-later

package cfg

// Package designed as easy to use interface for parse INI files

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/alonsovidales/otc/log"
	"github.com/alyu/configparser"
)

var cfg *configparser.Configuration
var sections = make(map[string]*configparser.Section)

// sectionsMu guards sections: it is filled lazily, on first use, from
// whichever goroutine reads a section first, and two goroutines writing a
// Go map at once kill the process (a fatal error no recover catches).
var sectionsMu sync.RWMutex

// Init Loads a INI file onto memory, first try to liad the config file from
// the etc/ directory on the current path, and if the file can't be found, try
// to load it from the /etc/ directory
// The name of the file to be used has to be the specified "appName_env.ini"
func Init(appName, env string) (err error) {
	// Trying to read the config file form the /etc directory on a first
	// instance
	if cfg, err = configparser.Read(fmt.Sprintf("etc/%s_%s.ini", appName, env)); err != nil {
		cfg, err = configparser.Read(fmt.Sprintf("/etc/%s_%s.ini", appName, env))
	}

	return
}

// GetStr Returns the value of the section, subsection as string
func GetStr(sec, subsec string) string {
	return loadSection(sec).ValueOf(subsec)
}

// GetUint64 Returns the value of the section, subsection as uint64
func GetUint64(sec, subsec string) (v uint64) {
	return uint64(GetInt(sec, subsec))
}

// GetInt Returns the value of the section, subsection as int64
func GetInt(sec, subsec string) (v int64) {
	if v, err := strconv.ParseInt(loadSection(sec).ValueOf(subsec), 10, 64); err == nil {
		return v
	}

	log.Error("Configuration parameter:", sec, subsec, "can't be parsed as integer")
	return
}

// GetFloat Returns the value of the section, subsection as float
func GetFloat(sec, subsec string) (v float64) {
	if v, err := strconv.ParseFloat(loadSection(sec).ValueOf(subsec), 64); err == nil {
		return v
	}

	log.Error("Configuration parameter:", sec, subsec, "can't be parsed as integer")
	return
}

// GetBool Returns the value of the section, subsection as boolean
func GetBool(sec, subsec string) (v bool) {
	vSec := loadSection(sec).ValueOf(subsec)

	return vSec == "1" || vSec == "true"
}

// HasSection reports whether a config section is present, without the
// log.Fatal loadSection triggers on a missing one - for genuinely optional
// sections (e.g. [apns], issue #43) that a caller needs to skip gracefully
// rather than crash the whole process when they're absent.
func HasSection(name string) bool {
	_, ok := cachedSection(name)
	return ok
}

// cachedSection returns the section and whether it exists, filling the
// cache on first use.
func cachedSection(name string) (*configparser.Section, bool) {
	sectionsMu.RLock()
	sec, ok := sections[name]
	sectionsMu.RUnlock()
	if ok {
		return sec, true
	}
	if cfg == nil {
		return nil, false
	}
	sec, err := cfg.Section(name) // configparser locks its own reads
	if err != nil {
		return nil, false
	}
	sectionsMu.Lock()
	if prev, ok := sections[name]; ok {
		sec = prev
	} else {
		sections[name] = sec
	}
	sectionsMu.Unlock()
	return sec, true
}

// loadSection loads a section of the config file
func loadSection(name string) (section *configparser.Section) {
	if section, ok := cachedSection(name); ok {
		return section
	}

	if cfg == nil {
		log.Fatal("Configuration file not yet loaded, call to the Init method before try to use the config manager")
	}
	log.Fatal("Configuration subsection:", name, "can't be parsed")

	return nil
}
