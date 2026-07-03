# VHS tape for the pgsavvy transaction-control feature GIF. -> docs/feat-tx.gif
# Recorded by `task demo:features`. Types a SELECT in the editor, begins a
# transaction (<leader>tb — toast confirms BEGIN), runs the statement inside
# the transaction (<leader>r — options bar shows "TX"), then commits
# (<leader>tc — toast confirms COMMIT). No data is mutated.
# Use only test fixture data — never record real databases.

Output docs/feat-tx.gif

Set Shell bash
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 12
Set TypingSpeed 60ms

# Launch, connect, and type the query off-camera (Hide) so the GIF opens with
# the query already in the editor.
Hide
Type "./bin/pgsavvy"
Enter
Sleep 2.5s
Enter
Sleep 2.5s
Type "3"
Sleep 600ms
Type "i"
Type "SELECT id, email, name FROM app.users ORDER BY id LIMIT 5;"
Sleep 700ms
Escape
Sleep 400ms
Show
Sleep 800ms

# Begin a transaction (<leader>tb). The options bar shows "TX" and a toast
# confirms "BEGIN".
Space
Type "tb"
Sleep 2s

# Run the statement inside the transaction (<leader>r), then focus the grid.
Space
Type "r"
Sleep 2.5s
Type "4"
Sleep 1.2s

# Back to the editor and commit the transaction (<leader>tc). Toast confirms
# "COMMIT" and the options bar drops the "TX" indicator.
Type "3"
Sleep 700ms
Space
Type "tc"
Sleep 2s
