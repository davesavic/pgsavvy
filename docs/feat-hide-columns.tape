# VHS tape for the pgsavvy hide-columns overlay feature GIF. -> docs/feat-hide-columns.gif
# Recorded by `task demo:features`. Runs a wide query over app.users (id,
# email, name, created_at, tags, data), focuses the grid, opens the
# hide-columns overlay (<leader>gH), toggles a column (space), moves through
# the list, and closes (q) — the grid re-renders without the hidden column.
# Use only test fixture data — never record real databases.

Output docs/feat-hide-columns.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 55ms

# Launch, connect, run the wide query, and focus the grid off-camera (Hide) so
# the GIF opens directly on the result grid.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name, created_at, tags, data FROM app.users ORDER BY id LIMIT 10;"
Sleep 700ms
Escape
Sleep 400ms
Space
Type "r"
Sleep 2.8s
Type "4"
Show
Sleep 900ms

# Open the hide-columns overlay (<leader>gH in RESULT_GRID scope).
Space
Type "gH"
Sleep 2.2s

# Navigate the column list (j/k), toggle one off (space).
Type "j"
Sleep 600ms
Type "j"
Sleep 600ms
Space
Sleep 1.5s

# Navigate to another column and toggle it off too.
Type "j"
Sleep 600ms
Space
Sleep 1.5s

# Close the overlay (q). The grid re-renders with the hidden columns removed.
Type "q"
Sleep 2s
