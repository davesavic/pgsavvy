# VHS tape for the pgsavvy demo GIF.
# Generate with: task demo   (builds bin/pgsavvy, brings up the docker fixture,
# writes an isolated demo connection profile, and records docs/demo.gif).
#
# This tape assumes `task demo` has exported XDG_CONFIG_HOME / XDG_STATE_HOME to
# an isolated .demo/ directory containing a single "demo" connection profile that
# points at the docker/postgres fixture (pgsavvy/pgsavvy@localhost:5432/pgsavvy_test,
# schema app: users/posts/user_roles from the demo seed).
#
# Timing is conservative; tune the Sleep values if your machine renders slower.

Output docs/demo.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch the TUI; the connection manager opens on the demo profile.
Type "./bin/pgsavvy"
Enter
Sleep 2.5s

# Connect to the selected profile.
Enter
Sleep 2.5s

# Browse the schema rails: Schemas (1) then Tables (2).
Type "1"
Sleep 1s
Type "2"
Sleep 1s
Type "j"
Sleep 600ms
Type "j"
Sleep 1s

# Jump to the query editor (3), enter insert mode, write a query.
Type "3"
Sleep 800ms
Type "i"
Type "SELECT id, email, name, created_at FROM app.users ORDER BY id LIMIT 25;"
Sleep 800ms
Escape
Sleep 500ms

# Run the statement under the cursor (<leader>r = Space r).
Space
Type "r"
Sleep 3s

# Back to the editor and show the EXPLAIN plan tree (<leader>e = Space e).
Type "3"
Sleep 500ms
Space
Type "e"
Sleep 3.5s

# Done.
Sleep 1s
