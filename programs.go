package main

import (
	"embed"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"retty/internal/agents"
	"retty/internal/rex"
)

//go:embed assets/icons/*.svg assets/brands/*.svg assets/agents/* assets/platforms/*
var assets embed.FS

var svgs = map[string]*ui.SVG{}
var programBitmaps = map[string]*ui.Bitmap{}

// programIcon shares vector and raster logos across tabs, the command palette
// and mobile lists. Raster logos keep their original colors.
func programIcon(c *ui.Context, name string) ui.Element {
	if strings.HasPrefix(name, "agent:") && strings.HasSuffix(name, ".png") {
		bitmap := programBitmaps[name]
		if bitmap == nil {
			data, err := assets.ReadFile("assets/agents/" + strings.TrimPrefix(name, "agent:"))
			if err != nil {
				panic(err)
			}
			bitmap, err = ui.DecodeBitmap(data)
			if err != nil {
				panic(err)
			}
			programBitmaps[name] = bitmap
		}
		return ui.Image(c, bitmap).Fit(ui.Contain)
	}
	switch name {
	case "agent:agy", "agent:dsh", "agent:reasonix", "agent:mistermorph", "agent:muse", "agent:empryo", "agent:minimax-code":
		return ui.Image(c, icon(name)).Fit(ui.Contain)
	}
	return ui.Icon(c, icon(name))
}

// icon returns an embedded SVG: "x" from the icons, "brand:git" from the
// brands.
func icon(name string) *ui.SVG {
	if s, ok := svgs[name]; ok {
		return s
	}
	file := "assets/icons/" + name + ".svg"
	if b, ok := strings.CutPrefix(name, "brand:"); ok {
		file = "assets/brands/" + b + ".svg"
	}
	if b, ok := strings.CutPrefix(name, "agent:"); ok {
		file = "assets/agents/" + b + ".svg"
	}
	if b, ok := strings.CutPrefix(name, "platform:"); ok {
		file = "assets/platforms/" + b + ".svg"
	}
	data, err := assets.ReadFile(file)
	if err != nil {
		panic(err)
	}
	s := ui.MustParseSVG(data)
	svgs[name] = s
	return s
}

// program describes a process and its icon in tabs and mobile session lists.
type program struct {
	Name      string
	Glyph     string
	IconColor ui.Color
	Shell     bool
	Agent     bool
	// LocalSelection prefers terminal copying in the editor's alternate screen.
	LocalSelection bool
}

func programIconColor(c *ui.Context, p program) ui.Color {
	color := p.IconColor
	if !c.Theme().Dark {
		if color == ui.Hex("#d5d7db") {
			color = colorsOf(c).text
		} else {
			color = color.Mix(ui.Hex("#000000"), 0.2)
		}
	}
	return color
}

// Titles stay stable while lifecycle indicators show activity separately.
// Codex's OSC spinner is presentation, and can outlive the actual turn.
func programTitle(title string, p program) string {
	title = strings.TrimSpace(title)
	if p.Glyph == "agent:codex" && title != "" {
		frame, size := utf8.DecodeRuneInString(title)
		if strings.ContainsRune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", frame) {
			rest := title[size:]
			next, _ := utf8.DecodeRuneInString(rest)
			if rest == "" || unicode.IsSpace(next) {
				title = strings.TrimSpace(rest)
			}
		}
	}
	if p.Glyph == "agent:claude" && strings.HasPrefix(title, "✳") {
		title = strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(title, "✳"), "\ufe0e\ufe0f"))
	}
	return title
}

var (
	shellProgram = program{Glyph: "square-terminal", IconColor: ui.Hex("#5fd38d"), Shell: true}

	programs = map[string]program{
		"node":      {Name: "Node", Glyph: "node-hex", IconColor: ui.Hex("#ffffff")},
		"bun":       {Name: "Bun", Glyph: "brand:bun", IconColor: ui.Hex("#3b2a20")},
		"deno":      {Name: "Deno", Glyph: "brand:deno", IconColor: ui.Hex("#ffffff")},
		"python":    {Name: "Python", Glyph: "brand:python", IconColor: ui.Hex("#ffd43b")},
		"lazygit":   {Name: "Git Changes", Glyph: "plus-minus-circle", IconColor: ui.Hex("#ffffff")},
		"tig":       {Name: "Git Log", Glyph: "git-branch", IconColor: ui.Hex("#ffffff")},
		"git":       {Name: "Git", Glyph: "brand:git", IconColor: ui.Hex("#ffffff")},
		"vim":       {Name: "Vim", Glyph: "brand:vim", IconColor: ui.Hex("#ffffff"), LocalSelection: true},
		"nvim":      {Name: "Neovim", Glyph: "brand:neovim", IconColor: ui.Hex("#ffffff"), LocalSelection: true},
		"hx":        {Name: "Helix", Glyph: "brand:helix", IconColor: ui.Hex("#c7a3f5")},
		"emacs":     {Name: "Emacs", Glyph: "brand:gnuemacs", IconColor: ui.Hex("#ffffff")},
		"htop":      {Name: "Activity", Glyph: "activity", IconColor: ui.Hex("#a7f3c4")},
		"btop":      {Name: "Activity", Glyph: "activity", IconColor: ui.Hex("#a7f3c4")},
		"top":       {Name: "Activity", Glyph: "activity", IconColor: ui.Hex("#a7f3c4")},
		"ssh":       {Name: "SSH", Glyph: "globe", IconColor: ui.Hex("#ffffff")},
		"mosh":      {Name: "Mosh", Glyph: "globe", IconColor: ui.Hex("#ffffff")},
		"docker":    {Name: "Docker", Glyph: "brand:docker", IconColor: ui.Hex("#ffffff")},
		"go":        {Name: "Go", Glyph: "brand:go", IconColor: ui.Hex("#ffffff")},
		"cargo":     {Name: "Cargo", Glyph: "brand:rust", IconColor: ui.Hex("#f4a261")},
		"rustc":     {Name: "Rust", Glyph: "brand:rust", IconColor: ui.Hex("#f4a261")},
		"npm":       {Name: "npm", Glyph: "brand:npm", IconColor: ui.Hex("#ffffff")},
		"pnpm":      {Name: "pnpm", Glyph: "brand:pnpm", IconColor: ui.Hex("#ffffff")},
		"yarn":      {Name: "Yarn", Glyph: "brand:yarn", IconColor: ui.Hex("#ffffff")},
		"ruby":      {Name: "Ruby", Glyph: "brand:ruby", IconColor: ui.Hex("#ffffff")},
		"irb":       {Name: "Ruby", Glyph: "brand:ruby", IconColor: ui.Hex("#ffffff")},
		"lua":       {Name: "Lua", Glyph: "brand:lua", IconColor: ui.Hex("#ffffff")},
		"php":       {Name: "PHP", Glyph: "brand:php", IconColor: ui.Hex("#ffffff")},
		"psql":      {Name: "PostgreSQL", Glyph: "brand:postgresql", IconColor: ui.Hex("#ffffff")},
		"mysql":     {Name: "MySQL", Glyph: "brand:mysql", IconColor: ui.Hex("#ffffff")},
		"redis-cli": {Name: "Redis", Glyph: "brand:redis", IconColor: ui.Hex("#ffffff")},
		"tmux":      {Name: "tmux", Glyph: "brand:tmux", IconColor: ui.Hex("#ffffff")},
		"make":      {Name: "Make", Glyph: "cpu", IconColor: ui.Hex("#ffffff")},
		"swift":     {Name: "Swift", Glyph: "brand:swift", IconColor: ui.Hex("#ffffff")},
		"kotlin":    {Name: "Kotlin", Glyph: "brand:kotlin", IconColor: ui.Hex("#ffffff")},
	}

	shells = map[string]bool{"zsh": true, "bash": true, "fish": true, "sh": true, "dash": true, "nu": true, "pwsh": true, "elvish": true, "xonsh": true, "tcsh": true, "csh": true, "ksh": true}
)

func init() {
	agentColors := map[string]string{
		"claude": "#d97757", "gemini": "#4d88e5", "copilot": "#748bfa",
		"aider": "#5faf87", "amp": "#e77343", "goose": "#e6ae51",
		"pi": "#b68be6", "omp": "#efb06a", "grok": "#aaaaaa",
		"qwen": "#9b7bee", "kimi": "#699ae7", "crush": "#e779c1",
		"codebuddy": "#7ca8ef", "qodercli": "#58cfa7", "qoderclicn": "#58cfa7", "traecli": "#62d49b",
	}
	for _, agent := range agents.All {
		glyph := "agent:" + agent.ID
		if agent.ID == "aider" || agent.ID == "ante" {
			glyph = "bot"
		} else if agent.ID == "atomcode" {
			glyph += ".png"
		}
		color := agentColors[agent.ID]
		if color == "" {
			color = "#d5d7db"
		}
		p := program{Name: agent.Name, Glyph: glyph, IconColor: ui.Hex(color), Agent: true}
		programs[agent.ID] = p
		for _, alias := range agent.Aliases {
			programs[alias] = p
		}
	}
	programs["python3"] = programs["python"]
	programs["ipython"] = programs["python"]
	programs["view"] = programs["vim"]
	programs["vi"] = programs["vim"]
	programs["less"] = program{Name: "Pager", Glyph: "file-code", IconColor: ui.Hex("#ffffff")}
	programs["man"] = programs["less"]
}

// sessionProgramName shares foreground process and launcher detection between
// desktop tabs and mobile lists, and is safe to persist without command arguments.
func sessionProgramName(info rex.SessionInfo) string {
	if info.Agent.Source == rex.ProgramStatusSource && info.Agent.ID != "" && info.Agent.ID != "终端程序" {
		return info.Agent.ID
	}
	if info.Idle || info.Program == "" {
		return info.Shell
	}
	if agent, ok := agents.Detect(info.Program, info.Args); ok {
		return agent.ID
	}
	return info.Program
}

// programOf returns how a program shows, by the name of its process.
func programOf(name string) program {
	name = strings.TrimPrefix(path.Base(name), "-")
	if p, ok := programs[strings.ToLower(name)]; ok {
		return p
	}
	if shells[name] {
		p := shellProgram
		p.Name = name
		return p
	}
	return program{Name: name, Glyph: "square-terminal", IconColor: ui.Hex("#ffffff")}
}
