// Package config defines the user-facing configuration schema for dbsavvy
// (UserConfig, ThemeConfig, KeybindingConfig) and the loaders that read it
// from YAML files via an afero.Fs. It also provides ParseKeyLabel and
// ParseKeySequence for raw key-label parsing, SafeText for sanitising
// user-supplied strings, and ValidateUserConfig for post-unmarshal
// validation.
//
// Validation accepts injected predicates (ValidationDeps) so pkg/config
// stays free of any pkg/gui/* imports per architectural decision D3.
package config
