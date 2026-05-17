// Package config defines the user-facing configuration schema for dbsavvy
// (UserConfig, ThemeConfig, KeybindingConfig) and the loaders that read it
// from YAML files via an afero.Fs. It also provides ParseKeyLabel for raw
// key-label parsing and ValidateUserConfig for post-unmarshal validation.
package config
