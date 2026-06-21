# VHS tape for the pgsavvy result-grid feature GIF. -> docs/feat-result-grid.gif
# Recorded by `task demo:features`. Shows a streamed result grid over the
# 500-row app.posts seed: in-grid search (/), column sort (<leader>s),
# expanded record view (<leader>gx), and the export menu (<leader>oe).
# Timing is conservative; tune the Sleep values if your machine renders slower.

Output docs/feat-result-grid.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch, connect, run the query, and focus the grid off-camera (Hide) so the
# GIF opens directly on the populated result grid.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, user_id, title, published, published_at FROM app.posts ORDER BY id;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 3s
Type "4"
Show
Sleep 1s

# In-grid search: jump to rows matching "Deep dive".
Type "/"
Sleep 500ms
Type "Deep dive"
Sleep 800ms
Enter
Sleep 1.5s
Type "n"
Sleep 1.2s
Escape
Sleep 800ms

# Sort by a column (<leader>s opens the column picker).
Space
Type "s"
Sleep 2.5s
Escape
Sleep 800ms

# Toggle the expanded single-record view (<leader>gx), then back to the grid.
Space
Type "gx"
Sleep 2.5s
Space
Type "gx"
Sleep 1.5s

# Open the export menu (<leader>oe): CSV / TSV / JSON to clipboard or file.
Space
Type "oe"
Sleep 3s
Escape
Sleep 1s
