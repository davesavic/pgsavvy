# VHS tape for the pgsavvy SQL omni-completion demo.
# Recorded by `task demo` with editor.autocomplete enabled. -> docs/demo-autocomplete.gif
#
# Shows the auto-triggered completion popup (after `FROM `), navigating the
# suggestion list, and accepting a candidate. Manual <c-x><c-o> behaves the
# same. Timing is conservative; tune the Sleep values if needed.

Output docs/demo-autocomplete.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 70ms

# Launch and connect.
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s

# Activate the app schema so completion has table/column metadata.
Type "1"
Sleep 700ms
Enter
Sleep 1.5s

# Query editor: type a partial statement. The completion popup auto-triggers
# after `FROM ` and lists the tables in the active search path.
Type "3"
Sleep 700ms
Type "i"
Type "SELECT * FROM "
Sleep 2.2s

# Move down the suggestion list (<c-n>/<c-p>), then accept with <c-y>.
Ctrl+N
Sleep 900ms
Ctrl+N
Sleep 900ms
Ctrl+Y
Sleep 1.8s

# Run the completed statement.
Escape
Sleep 400ms
Space
Type "r"
Sleep 3.5s
