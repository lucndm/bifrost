package tokensaver

import (
	"path"
	"sort"
)

type resolvedSettings struct {
	RTK      bool
	Caveman  string
	Ponytail string
}

func mergeSettings(base resolvedSettings, override Settings) resolvedSettings {
	if override.RTK != nil {
		base.RTK = *override.RTK
	}
	if override.Caveman != nil {
		base.Caveman = *override.Caveman
	}
	if override.Ponytail != nil {
		base.Ponytail = *override.Ponytail
	}
	return base
}

func (p *Plugin) resolveSettings(model, virtualKey string) resolvedSettings {
	settings := p.defaultSettings
	if override, ok := p.virtualKeys[virtualKey]; ok && virtualKey != "" {
		settings = mergeSettings(settings, override)
	}
	if pattern, ok := p.matchModelPattern(model); ok {
		settings = mergeSettings(settings, p.models[pattern])
	}
	return settings
}

func (p *Plugin) matchModelPattern(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	matches := make([]string, 0)
	for pattern := range p.models {
		matched, err := path.Match(pattern, model)
		if err == nil && matched {
			matches = append(matches, pattern)
		}
	}
	if len(matches) == 0 {
		return "", false
	}
	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i]) == len(matches[j]) {
			return matches[i] < matches[j]
		}
		return len(matches[i]) > len(matches[j])
	})
	return matches[0], true
}
