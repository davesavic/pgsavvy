# VHS tape for the pgsavvy cell-viewer feature GIF. -> docs/feat-cell-viewer.gif
# Recorded by `task demo:features`. Runs a query over app.users (which has a
# jsonb data column), focuses the grid, moves onto the data column of the
# first row, and opens the cell viewer (<leader>gv) to show the full JSON
# content with scroll, wrap toggle, and pretty-print.
# Use only test fixture data — never record real databases.

Output docs/feat-cell-viewer.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, run query, and focus the grid off-camera (Hide) so the GIF
# opens directly on the populated result grid.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name, data FROM app.users ORDER BY id LIMIT 5;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Move onto the jsonb "data" column (rightmost).
Type "l"
Sleep 350ms
Type "l"
Sleep 350ms
Type "l"
Sleep 700ms

# Open the cell viewer (<leader>gv) to see the full JSON content.
Space
Type "gv"
Sleep 2.5s

# Scroll within the viewer.
Type "j"
Sleep 500ms
Type "j"
Sleep 500ms
Type "k"
Sleep 500ms

# Toggle line wrapping (w), then pretty-print JSON (p).
Type "w"
Sleep 1.8s
Type "p"
Sleep 2.2s

# Close the viewer.
Type "q"
Sleep 800ms
