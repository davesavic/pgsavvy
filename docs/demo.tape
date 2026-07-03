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

# Launch + connect off-camera (Hide) so the GIF opens on the live session.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Show
Sleep 600ms

# Focus the schema-rail (Tab), switch to its Tables tab (Type "]"),
# then scroll through the table list with j.
Tab
Sleep 1.2s
Enter
Sleep 1.2s
Type "j"
Sleep 500ms
Type "j"
Sleep 500ms
Type "j"
Sleep 1.2s

# Switch to the query editor (3), enter insert mode, write a query.
# Auto-trigger opens the completion popup after `FROM `.
# Navigate to `users` with Ctrl+N then accept with Ctrl+Y.
Type "3"
Sleep 800ms
Type "i"
Type "SELECT * FROM "
Sleep 2.2s
Ctrl+N
Sleep 500ms
Ctrl+N
Sleep 500ms
Ctrl+N
Sleep 500ms
Ctrl+N
Sleep 500ms
Ctrl+N
Sleep 500ms
Ctrl+N
Sleep 500ms
Ctrl+Y
Sleep 1.2s
Type " ORDER BY id LIMIT 25;"
Sleep 800ms
Escape
Sleep 500ms

# Run the statement under the cursor (<leader>r = Space r).
Space
Type "r"
Sleep 3s

# Back to the editor and show the EXPLAIN plan tree (<leader>e = Space e).
Type "3"
Sleep 600ms
Space
Type "e"
Sleep 5s
