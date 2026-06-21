# VHS tape for the pgsavvy inline cell-editing feature GIF. -> docs/feat-cell-edit.gif
# Recorded by `task demo:features`. Edits a cell in app.users, stages a pending
# edit, opens the commit dialog (<leader>cw), toggles the SQL preview (s), runs a
# dry-run (d), then cancels (esc) — so re-recording never mutates the fixture.

Output docs/feat-cell-edit.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch, connect, query a writable table, and focus the grid off-camera (Hide)
# so the GIF opens directly on the editable result grid.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name FROM app.users ORDER BY id LIMIT 20;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Move onto the editable "name" column.
Type "l"
Sleep 350ms
Type "l"
Sleep 700ms

# Open the cell editor (i), append to the value, stage the edit (Enter).
Type "i"
Sleep 1.2s
Type " (edited)"
Sleep 1s
Enter
Sleep 1.5s

# Open the commit dialog to review staged pending edits.
Space
Type "cw"
Sleep 2.5s

# Toggle the SQL preview, then dry-run the change in a rolled-back transaction.
Type "s"
Sleep 2.5s
Type "d"
Sleep 2.8s

# Cancel without applying.
Escape
Sleep 1s
