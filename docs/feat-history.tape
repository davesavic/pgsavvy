# VHS tape for the pgsavvy query-history feature GIF. -> docs/feat-history.gif
# Recorded by `task demo:features`. Types three statements (one per line), runs
# each (statement-under-cursor) to populate the SQLite-backed history, then opens
# the History tab (<leader>h) and navigates the recall list.

Output docs/feat-history.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, and pre-populate the history off-camera (Hide) by running a
# few statements, so the GIF opens directly on the recall list.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s

# Type three statements, one per line.
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name FROM app.users ORDER BY id LIMIT 5;"
Enter
Type "SELECT count(*) AS posts FROM app.posts WHERE published;"
Enter
Type "SELECT id, name FROM app.roles ORDER BY id;"
Escape
Sleep 500ms

# Run each statement-under-cursor (refocus editor with 3 after each run).
Type "gg"
Sleep 400ms
Space
Type "r"
Sleep 1.8s
Type "3"
Sleep 700ms
Type "j"
Sleep 300ms
Space
Type "r"
Sleep 1.8s
Type "3"
Sleep 700ms
Type "j"
Sleep 300ms
Space
Type "r"
Sleep 1.8s

# Reveal from here: open the History tab and walk the recall list.
Type "3"
Sleep 700ms
Show
Sleep 500ms
Space
Type "h"
Sleep 2.5s
Type "j"
Sleep 900ms
Type "j"
Sleep 900ms
Type "k"
Sleep 1.8s
