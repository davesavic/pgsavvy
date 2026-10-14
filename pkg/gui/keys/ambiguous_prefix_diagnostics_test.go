package keys

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func TestAmbiguousPrefixDiagnostics_BindingsWithoutOrigins(t *testing.T) {
	for _, tc := range []struct {
		prefix, descendant, scope, action, descendantAction string
	}{
		{"y", "yy", "result_grid", commands.ResultYankCell, commands.ResultYankRow},
		{"<space>", "<leader>p", "connection_manager", commands.ConnectionManagerToggle, commands.ConnectionManagerPasteDSN},
		{"g", "gxyz", "result_grid", "test.prefix", "test.descendant"},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			cfg := config.GetDefaultConfig()
			cfg.Keybindings = nil
			registry := commands.NewRegistry()
			var defaults []*ChordBinding
			for _, spec := range []struct{ key, action string }{{tc.prefix, tc.action}, {tc.descendant, tc.descendantAction}} {
				require.NoError(t, registry.Register(fixtureCmd(spec.action)))
				seq, err := SequenceFromShorthand(spec.key)
				require.NoError(t, err)
				defaults = append(defaults, &ChordBinding{Sequence: seq, Mode: types.ModeNormal, Scope: types.ContextKey(tc.scope), ActionID: spec.action})
			}
			trieSet, diagnostics, err := NewKeybindingService().Build(defaults, cfg, registry, nil)
			require.NoError(t, err)
			require.Len(t, diagnostics, 1)
			diagnostic := diagnostics[0]
			require.Equal(t, "ambiguous_prefix", diagnostic.Code)
			require.Equal(t, InfoLevel, diagnostic.Level)
			require.Equal(t, "shipped default", diagnostic.Origin)
			require.Contains(t, diagnostic.Message, "action="+tc.action)
			require.Contains(t, diagnostic.Message, tc.descendantAction)
			require.Contains(t, diagnostic.Message, "scope="+tc.scope)
			require.Contains(t, diagnostic.Message, "mode=normal")
			require.Contains(t, diagnostic.Message, "shipped-default overlap")
			require.Contains(t, diagnostic.Message, "vim timeoutlen rule applies")
			require.NotContains(t, diagnostic.Message, "descendants: []")
			descendant := expandLeaderTokens(defaults[1].Sequence, ' ', ',')
			require.Contains(t, diagnostic.Message, `"`+SequenceString(descendant)+`"`)
			trie, ok := trieSet.Get(types.ModeNormal, types.ContextKey(tc.scope))
			require.True(t, ok)
			leaf := trie.Lookup(expandLeaderTokens(defaults[0].Sequence, ' ', ','))
			require.True(t, leaf.IsLeaf && leaf.HasChildren)
			require.Empty(t, leaf.Origin, "diagnostic fallback must not change binding provenance")
			require.Equal(t, tc.descendantAction, trie.Lookup(descendant).Action.ID)
		})
	}
}

func TestAmbiguousPrefixDiagnostics_UserBindings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		userPrefix bool
		source     Source
		origin     string
	}{
		{"user prefix", true, UserOverride, ""},
		{"custom prefix", true, CustomCmd, ""},
		{"user descendant", false, UserOverride, ""},
		{"custom descendant", false, CustomCmd, ""},
		{"populated origins", false, UserOverride, "config.yml:7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builder := NewTrieBuilder()
			prefix := &ChordBinding{Sequence: []Key{{Code: 'y'}}, ActionID: "test.cell", Origin: tc.origin}
			descendant := &ChordBinding{Sequence: []Key{{Code: 'y'}, {Code: 'y'}}, ActionID: "test.row", Origin: tc.origin}
			if tc.userPrefix {
				builder.InsertDefault(descendant, fixtureCmd(descendant.ActionID))
				prefix.Source = tc.source
				if tc.source == CustomCmd {
					prefix.ActionID = "command:echo sensitive-value"
				}
				builder.InsertUser(prefix, fixtureCmd(prefix.ActionID))
			} else {
				builder.InsertDefault(prefix, fixtureCmd(prefix.ActionID))
				descendant.Source = tc.source
				if tc.source == CustomCmd {
					descendant.ActionID = "command:echo sensitive-value"
				}
				builder.InsertUser(descendant, fixtureCmd(descendant.ActionID))
			}
			_, diagnostics := builder.Build()
			require.Len(t, diagnostics, 1)
			require.Equal(t, WarnLevel, diagnostics[0].Level)
			require.NotEmpty(t, diagnostics[0].Origin)
			require.Contains(t, diagnostics[0].Message, "user-binding overlap; review configuration")
			if tc.origin != "" {
				require.Equal(t, tc.origin, diagnostics[0].Origin)
				require.Contains(t, diagnostics[0].Message, tc.origin)
			}
			if tc.source == CustomCmd {
				require.Contains(t, diagnostics[0].Message, "custom command")
				require.NotContains(t, diagnostics[0].Message, "sensitive-value")
			}
		})
	}
}

func TestAmbiguousPrefixDiagnostics_UserBeyondDisplayLimit(t *testing.T) {
	builder := NewTrieBuilder()
	builder.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'g'}}}, fixtureCmd("test.prefix"))
	for _, suffix := range "edcba" {
		builder.InsertDefault(&ChordBinding{Sequence: []Key{{Code: 'g'}, {Code: suffix}}}, fixtureCmd("test."+string(suffix)))
	}
	builder.InsertUser(&ChordBinding{Sequence: []Key{{Code: 'g'}, {Code: 'z'}}, Source: UserOverride}, fixtureCmd("test.user"))
	_, diagnostics := builder.Build()
	require.Len(t, diagnostics, 1)
	require.Equal(t, WarnLevel, diagnostics[0].Level)
	require.Contains(t, diagnostics[0].Message, `descendants: ["ga" -> test.a (shipped default), "gb" -> test.b (shipped default), "gc" -> test.c (shipped default), "gd" -> test.d (shipped default), ...]`)
	require.NotContains(t, diagnostics[0].Message, "test.user")
	require.Contains(t, diagnostics[0].Message, "user-binding overlap")
}
