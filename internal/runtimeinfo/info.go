// Package runtimeinfo describes the running binary without executing a tool or
// consulting a release server. Missing information stays explicitly unknown.
package runtimeinfo

import (
	"runtime"
	"runtime/debug"
	"sync"
)

// Version is initialized by the desktop/CLI entrypoint before starting workers.
var Version string

type Info struct {
	Version  string `json:"version,omitempty"`
	Revision string `json:"revision,omitempty"`
	Modified bool   `json:"modified,omitempty"`
	MyGo     string `json:"mygo,omitempty"`
	Go       string `json:"go,omitempty"`
}

// Build metadata belongs to the executable and cannot change while it runs.
// Keep the entrypoint's Version separate: it can be set after an early query.
var executableInfo = sync.OnceValue(readExecutableInfo)

func Current() Info {
	r := executableInfo()
	r.Version = Version
	return r
}

func readExecutableInfo() Info {
	r := Info{Go: runtime.Version()}
	if b, ok := debug.ReadBuildInfo(); ok {
		for _, s := range b.Settings {
			switch s.Key {
			case "vcs.revision":
				r.Revision = s.Value
			case "vcs.modified":
				r.Modified = s.Value == "true"
			}
		}
		for _, d := range b.Deps {
			if d.Path == "github.com/egoist/mygo" {
				r.MyGo = d.Version
				if d.Replace != nil {
					r.MyGo = d.Replace.Path + "@" + d.Replace.Version
				}
			}
		}
	}
	return r
}
