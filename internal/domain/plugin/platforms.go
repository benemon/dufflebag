package plugin

// KnownPlatform reports whether platform is one HashiCorp builds Packer
// plugins for (Platforms, from spec/platforms.json). The organization's
// default platforms are chosen from this list; an import still takes whatever
// platforms a release actually published.
func KnownPlatform(platform string) bool {
	for _, known := range Platforms {
		if known == platform {
			return true
		}
	}
	return false
}
