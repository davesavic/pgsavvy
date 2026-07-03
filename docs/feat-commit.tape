# VHS tape for the pgsavvy commit-dialog feature GIF. -> docs/feat-commit.gif
# Recorded by `task demo:features`. Queries app.users, edits two cells in the
# "name" column, opens the commit dialog (<leader>cw) to review the staged
# edits, toggles the SQL preview (s), runs a dry-run (d), then cancels
# without applying — so re-recording never mutates the fixture.
# Use only test fixture data — never record real databases.

Output docs/feat-commit.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, query a writable table, and focus the grid off-camera
# (Hide) so the GIF opens directly on the editable result grid.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name FROM app.users ORDER BY id LIMIT 10;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Move onto the "name" column (third column).
Type "l"
Sleep 350ms
Type "l"
Sleep 700ms

# Edit the first cell: open editor (i), append text, stage (Enter).
Type "i"
Sleep 1s
Type " (first)"
Sleep 800ms
Enter
Sleep 1.2s

# Move down and edit a second cell.
Type "j"
Sleep 700ms
Type "i"
Sleep 1s
Type " (second)"
Sleep 800ms
Enter
Sleep 1.2s

# Open the commit dialog (<leader>cw) to review both staged edits.
Space
Type "cw"
Sleep 2.5s

# Walk the edit list (j).
Type "j"
Sleep 1.2s

# Toggle the generated SQL preview (s), then dry-run the statements (d).
Type "s"
Sleep 2.5s
Type "d"
Sleep 2.8s

# Cancel without applying — no fixture mutation.
Escape
Sleep 1s
