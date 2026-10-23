// Package runtimeinfo describes the running binary without executing a tool or
// consulting a release server. Missing information stays explicitly unknown.
package runtimeinfo

import (
	"runtime"
	"runtime/debug"
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

func Current() Info {
	r := Info{Version: Version, Go: runtime.Version()}
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
