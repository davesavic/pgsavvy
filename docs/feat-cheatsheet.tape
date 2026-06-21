# VHS tape for the pgsavvy cheatsheet feature GIF. -> docs/feat-cheatsheet.gif
# Recorded by `task demo:features`. Opens the auto-generated, tabbed cheatsheet
# (?) and cycles its tabs (Tab) with a little scrolling (j).

Output docs/feat-cheatsheet.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch and connect off-camera (Hide) so the GIF opens on the feature itself.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Show

# Open the cheatsheet.
Type "?"
Sleep 2.8s

# Cycle through tabs.
Tab
Sleep 2s
Tab
Sleep 2s
Type "j"
Sleep 500ms
Type "j"
Sleep 500ms
Type "j"
Sleep 800ms
Tab
Sleep 2s

# Close.
Type "q"
Sleep 800ms
