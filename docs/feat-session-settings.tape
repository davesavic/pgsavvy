# VHS tape for the pgsavvy session-settings feature GIF. -> docs/feat-session-settings.gif
# Recorded by `task demo:features`. Demonstrates three session-management
# operations: quick-setting the search_path (<leader>p), setting
# statement_timeout (<leader>tt), and hot-reloading the user config with
# :reload. Each operation produces an inline toast.
# Use only test fixture data — never record real databases.

Output docs/feat-session-settings.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch and connect off-camera (Hide) so the GIF opens on the live session.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Show
Sleep 800ms

# Quick-set search_path (<leader>p). Opens a prompt pre-filled with
# "SET search_path TO". Append "public" and submit.
Space
Type "p"
Sleep 1s
Type "public"
Sleep 600ms
Enter
Sleep 2s

# Set statement_timeout (<leader>tt). Opens a prompt for a pg-style duration,
# validates, and executes SET statement_timeout on the session.
Space
Type "tt"
Sleep 800ms
Type "30s"
Sleep 500ms
Enter
Sleep 2s

# Open the ex-command line (:) and issue :reload to hot-reload the user config
# (keybindings, theme, UI settings) without restarting. Toast confirms.
Type ":"
Sleep 600ms
Type "reload"
Sleep 500ms
Enter
Sleep 2.5s
