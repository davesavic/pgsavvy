# VHS tape for the pgsavvy result-export feature GIF. -> docs/feat-export.gif
# Recorded by `task demo:features`. Runs a query over app.posts, focuses the
# result grid, opens the export menu (<leader>oe), cycles format (JSON →
# Markdown → SQL INSERTs) and destination, then cancels so the GIF never
# writes a real file. Use only test fixture data — never record real databases.

Output docs/feat-export.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

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
Type "SELECT id, user_id, title, published FROM app.posts ORDER BY id LIMIT 10;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Open the export menu (<leader>oe). The menu shows format selector and
# destination (file / clipboard / stdout) with editable file path.
Space
Type "oe"
Sleep 2.8s

# Cycle the format field: defaults to CSV, left/right changes it.
Type "j"
Sleep 600ms
Right
Sleep 1.2s
Right
Sleep 1.2s
Right
Sleep 1.2s
Right
Sleep 1.2s
Right
Sleep 1.2s

# Close the menu without exporting.
Escape
Sleep 1s
